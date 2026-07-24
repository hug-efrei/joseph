package main

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// Configuration par défaut
var (
	Port         = getEnv("PORT", "8080")
	CacheDir     = getEnv("CACHE_DIR", "/data/cache/covers")
	BooksPerPage = getEnvInt("BOOKS_PER_PAGE", 24)
)

// Book est le modèle affiché par les templates. Il est reconstruit à chaque
// requête à partir d'une entrée du flux OPDS BookOrbit — voir bookorbit.go.
type Book struct {
	ID            int
	Title         string
	Author        string // tous les auteurs joints par " & ", pour l'affichage
	PrimaryAuthor string // premier auteur, utilisé pour le lien "parcourir par auteur"
	Series        string
	SeriesID      int
	SeriesIndex   float64
	Description   string
	HasKepub      bool
	Files         []BookFile
}

type BookFile struct {
	ID     int
	Format string // "epub", "kepub", "pdf", ... (tel que renvoyé par BookOrbit)
}

// CompactLogger est un middleware Gin minimaliste pour Docker
func CompactLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		cacheStatus, _ := c.Get("cache_status")
		tag := ""
		if cacheStatus != nil {
			tag = fmt.Sprintf(" [%s]", cacheStatus)
		}

		// Format ultra-compact : HH:MM:SS | STATUS | METHOD | LATENCY | PATH [TAG]
		fmt.Printf("%s | %d | %s | %v | %s%s\n",
			time.Now().Format("15:04:05"),
			status,
			c.Request.Method,
			latency.Round(time.Microsecond),
			c.Request.URL.RequestURI(),
			tag,
		)
	}
}

func main() {
	bookorbitURL := requireEnv("BOOKORBIT_URL")
	bookorbitUser := requireEnv("BOOKORBIT_OPDS_USER")
	bookorbitPassword := requireEnv("BOOKORBIT_OPDS_PASSWORD")

	if err := os.MkdirAll(CacheDir, 0755); err != nil {
		log.Printf("⚠️  ERREUR : Impossible de créer le dossier cache : %v", err)
	} else {
		log.Printf("✅ Dossier cache prêt : %s", CacheDir)
	}

	client := newOpdsClient(bookorbitURL, bookorbitUser, bookorbitPassword)
	if err := client.Ping(); err != nil {
		log.Printf("⚠️  BookOrbit (%s) injoignable au démarrage : %v — nouvelle tentative à la prochaine requête", bookorbitURL, err)
	} else {
		log.Printf("✅ Connecté à BookOrbit : %s", bookorbitURL)
	}

	cache := newBookCache(5000)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New() // Pas de logger par défaut
	r.Use(CompactLogger(), gin.Recovery())

	r.SetFuncMap(template.FuncMap{
		"safe": func(s string) template.HTML { return template.HTML(s) },
	})

	r.LoadHTMLGlob("templates/*")

	// Route Accueil (Liste intelligente)
	r.GET("/", func(c *gin.Context) {
		query := c.Query("q")
		author := c.Query("author")
		seriesIDStr := c.Query("series_id")
		pageStr := c.Query("page")
		searchMode := c.Query("search_mode") // Toggle barre de recherche
		lastPage := c.Query("last_page")     // Mémoire navigation

		// Détection du terminal pour adapter la pagination
		userAgent := c.GetHeader("User-Agent")
		pageSize := BooksPerPage // Défaut desktop (24)

		uaLower := strings.ToLower(userAgent)
		isLimited := strings.Contains(uaLower, "kobo") ||
			strings.Contains(uaLower, "mobile") ||
			strings.Contains(uaLower, "android") ||
			strings.Contains(uaLower, "kindle") ||
			strings.Contains(uaLower, "ipad") ||
			strings.Contains(uaLower, "iphone")

		if isLimited {
			pageSize = 8 // 2 lignes sur Kobo (4 cols) ou 4 lignes sur Mobile (2 cols)
		}

		page, _ := strconv.Atoi(pageStr)
		if page < 1 {
			page = 1
		}
		seriesID, _ := strconv.Atoi(seriesIDStr)

		books, total, err := client.fetchCatalog(catalogParams{
			Page:     page,
			Size:     pageSize,
			Query:    query,
			Author:   author,
			SeriesID: seriesID,
		})
		if err != nil {
			log.Printf("bookorbit: %v", err)
			c.String(http.StatusBadGateway, "BookOrbit est injoignable, réessaie dans un instant.")
			return
		}
		cache.PutAll(books)

		hasNext := page*pageSize < total
		showSearch := searchMode == "true"

		c.HTML(http.StatusOK, "index.html", gin.H{
			"Books": books, "Query": query,
			"Author": author, "SeriesID": seriesIDStr,
			"Page": page, "HasNext": hasNext, "PrevPage": page - 1, "NextPage": page + 1,
			"ShowSearch": showSearch,
			"LastPage":   lastPage,
		})
	})

	// Route Détails
	r.GET("/book/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		book, ok := cache.Get(id)
		if !ok {
			c.String(http.StatusNotFound, "Livre introuvable — reviens à la liste et réessaie (le cache a peut-être expiré).")
			return
		}

		backQuery := c.Query("q")
		backPage := c.Query("page")
		backSearchMode := c.Query("search_mode")

		c.HTML(http.StatusOK, "book.html", gin.H{
			"Book":           book,
			"Description":    template.HTML(book.Description),
			"BackQuery":      backQuery,
			"BackPage":       backPage,
			"BackSearchMode": backSearchMode,
		})
	})

	// Route Image de couverture (proxy + cache disque de la miniature BookOrbit)
	r.GET("/cover/:id", func(c *gin.Context) {
		id := c.Param("id")
		cachePath := filepath.Join(CacheDir, id+".jpg")

		if _, err := os.Stat(cachePath); err == nil {
			c.Set("cache_status", "H") // HIT
			c.Header("Cache-Control", "public, max-age=604800")
			c.File(cachePath)
			return
		}

		c.Set("cache_status", "M") // MISS par défaut

		resp, err := client.proxyGet("/api/v1/opds/"+id+"/thumbnail", nil)
		if err != nil {
			c.Set("cache_status", "X")
			c.Status(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			c.Set("cache_status", "X")
			c.Status(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Set("cache_status", "X")
			c.Status(http.StatusBadGateway)
			return
		}

		if err := os.WriteFile(cachePath, body, 0644); err == nil {
			c.Set("cache_status", "C") // OK CACHED
		} else {
			c.Set("cache_status", "X")
		}

		c.Header("Cache-Control", "public, max-age=604800")
		c.Data(http.StatusOK, "image/jpeg", body)
	})

	// Route Download — proxy vers BookOrbit, en choisissant le fichier selon
	// la priorité de format demandée (Kobo → kepub sinon epub, Standard →
	// l'inverse). BookOrbit gère lui-même Content-Type/Content-Disposition.
	r.GET("/download/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		mode := c.Query("mode")

		book, ok := cache.Get(id)
		if !ok {
			c.String(http.StatusNotFound, "Livre introuvable — reviens à la liste et réessaie.")
			return
		}

		fileID, found := pickFile(book.Files, mode == "kepub")
		if !found {
			c.String(http.StatusNotFound, "Aucun fichier disponible pour ce livre")
			return
		}

		resp, err := client.proxyGet(fmt.Sprintf("/api/v1/opds/%d/download?fileId=%d", id, fileID), nil)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			c.Status(resp.StatusCode)
			return
		}

		for _, h := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
			if v := resp.Header.Get(h); v != "" {
				c.Header(h, v)
			}
		}
		c.Status(http.StatusOK)
		io.Copy(c.Writer, resp.Body)
	})

	srv := &http.Server{
		Addr:              ":" + Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute, // laisse le temps aux gros téléchargements epub/kepub sur liaison lente (Kobo)
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("erreur serveur HTTP : %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("arrêt en cours…")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("arrêt forcé du serveur : %v", err)
	}
	log.Println("serveur arrêté proprement")
}

// pickFile choisit le fichier à télécharger selon la priorité de format :
// preferKepub=true  → kepub, sinon epub, sinon le premier disponible.
// preferKepub=false → epub, sinon kepub, sinon le premier disponible.
func pickFile(files []BookFile, preferKepub bool) (int, bool) {
	first, second := "epub", "kepub"
	if preferKepub {
		first, second = "kepub", "epub"
	}
	if id, ok := findFormat(files, first); ok {
		return id, true
	}
	if id, ok := findFormat(files, second); ok {
		return id, true
	}
	if len(files) > 0 {
		return files[0].ID, true
	}
	return 0, false
}

func findFormat(files []BookFile, format string) (int, bool) {
	for _, f := range files {
		if strings.EqualFold(f.Format, format) {
			return f.ID, true
		}
	}
	return 0, false
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("⚠️  %s invalide (%q), valeur par défaut %d utilisée", key, value, fallback)
		return fallback
	}
	return n
}

func requireEnv(key string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		log.Fatalf("Erreur : variable d'environnement %s manquante", key)
	}
	return value
}
