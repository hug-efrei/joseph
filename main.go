package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nfnt/resize"
)

// Helper pour le recadrage intelligent (Smart Crop)
type SubImager interface {
	SubImage(r image.Rectangle) image.Image
}

// Configuration par défaut
var (
	LibraryPath  = getEnv("LIBRARY_PATH", "/books")
	Port         = getEnv("PORT", "8080")
	BooksPerPage = 24
)

type Book struct {
	ID          int
	Title       string
	Author      string
	AuthorID    int // NOUVEAU : Pour le lien
	Path        string
	Series      string
	SeriesID    int // NOUVEAU : Pour le lien
	SeriesIndex float64
	Description string
	HasKepub    bool
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
	LibraryPath = getEnv("LIBRARY_PATH", "/books")

	// Initialisation du dossier de cache (Indispensable pour la persistance via volumes)
	cacheDir := filepath.Join(LibraryPath, ".cache", "covers")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("⚠️  ERREUR : Impossible de créer le dossier cache : %v", err)
	} else {
		log.Printf("✅ Dossier cache prêt : %s", cacheDir)
	}

	dbPath := filepath.Join(LibraryPath, "metadata.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Fatalf("Erreur : Aucune DB trouvée à %s", dbPath)
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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
		authorID := c.Query("author_id") // NOUVEAU
		seriesID := c.Query("series_id") // NOUVEAU
		pageStr := c.Query("page")
		searchMode := c.Query("search_mode") // NOUVEAU : Toggle barre de recherche

		// Détection du terminal pour adapter la pagination
		userAgent := c.GetHeader("User-Agent")
		pageSize := BooksPerPage // Défaut desktop (24)

		// Liste de mots-clés pour terminaux mobiles/ereaders
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
		offset := (page - 1) * pageSize

		// Requête de base avec déduplication des auteurs via GROUP_CONCAT
		baseQuery := `
			SELECT 
				b.id, b.title, GROUP_CONCAT(a.name, ' & '), a.id, b.path, s.name, s.id, b.series_index,
				(SELECT COUNT(*) FROM data WHERE book = b.id AND format = 'KEPUB') > 0 as has_kepub
			FROM books b
			JOIN books_authors_link bal ON b.id = bal.book
			JOIN authors a ON bal.author = a.id
			LEFT JOIN books_series_link bsl ON b.id = bsl.book
			LEFT JOIN series s ON bsl.series = s.id
			WHERE b.id IN (SELECT book FROM data WHERE format = 'EPUB' OR format = 'KEPUB')
		`

		args := []interface{}{}

		// Gestion des filtres
		if query != "" {
			baseQuery += " AND (b.title LIKE ? OR a.name LIKE ?)"
			args = append(args, "%"+query+"%", "%"+query+"%")
		}
		if authorID != "" {
			baseQuery += " AND a.id = ?"
			args = append(args, authorID)
		}
		if seriesID != "" {
			baseQuery += " AND s.id = ?"
			args = append(args, seriesID)
		}

		// Groupement pour éviter les doublons
		baseQuery += " GROUP BY b.id"

		// TRI INTELLIGENT :
		// Si on regarde une série, on veut l'ordre 1, 2, 3...
		// Sinon on veut les derniers ajouts.
		if seriesID != "" {
			baseQuery += " ORDER BY b.series_index ASC"
		} else {
			baseQuery += " ORDER BY b.id DESC"
		}

		baseQuery += " LIMIT ? OFFSET ?"
		args = append(args, pageSize+1, offset)

		rows, err := db.Query(baseQuery, args...)
		if err != nil {
			c.String(500, "Erreur DB: "+err.Error())
			return
		}
		defer rows.Close()

		var books []Book
		for rows.Next() {
			var b Book
			var seriesName sql.NullString
			var seriesID sql.NullInt64
			var seriesIndex sql.NullFloat64

			rows.Scan(&b.ID, &b.Title, &b.Author, &b.AuthorID, &b.Path, &seriesName, &seriesID, &seriesIndex, &b.HasKepub)

			if seriesName.Valid {
				b.Series = seriesName.String
				b.SeriesID = int(seriesID.Int64)
				b.SeriesIndex = seriesIndex.Float64
			}
			books = append(books, b)
		}

		hasNext := false
		if len(books) > pageSize {
			hasNext = true
			books = books[:pageSize]
		}

		// Define showSearch based on explicit mode only
		showSearch := searchMode == "true"

		// On passe les filtres actuels au template pour la pagination
		c.HTML(200, "index.html", gin.H{
			"Books": books, "Query": query,
			"AuthorID": authorID, "SeriesID": seriesID,
			"Page": page, "HasNext": hasNext, "PrevPage": page - 1, "NextPage": page + 1,
			"ShowSearch": showSearch, // NOUVEAU
		})
	})

	// Route Détails
	r.GET("/book/:id", func(c *gin.Context) {
		id := c.Param("id")
		query := `
			SELECT 
				b.id, b.title, a.name, a.id, b.path, s.name, s.id, b.series_index, c.text,
				(SELECT COUNT(*) FROM data WHERE book = b.id AND format = 'KEPUB') > 0 as has_kepub
			FROM books b
			JOIN books_authors_link bal ON b.id = bal.book
			JOIN authors a ON bal.author = a.id
			LEFT JOIN books_series_link bsl ON b.id = bsl.book
			LEFT JOIN series s ON bsl.series = s.id
			LEFT JOIN comments c ON b.id = c.book
			WHERE b.id = ?
		`
		row := db.QueryRow(query, id)

		var b Book
		var seriesName sql.NullString
		var seriesID sql.NullInt64
		var seriesIndex sql.NullFloat64
		var description sql.NullString

		err := row.Scan(&b.ID, &b.Title, &b.Author, &b.AuthorID, &b.Path, &seriesName, &seriesID, &seriesIndex, &description, &b.HasKepub)
		if err != nil {
			c.String(404, "Livre introuvable")
			return
		}

		// Capture Context params so we can go back
		backQuery := c.Query("q")
		backPage := c.Query("page")
		backSearchMode := c.Query("search_mode")

		if seriesName.Valid {
			b.Series = seriesName.String
			b.SeriesID = int(seriesID.Int64)
			b.SeriesIndex = seriesIndex.Float64
		}
		if description.Valid {
			b.Description = description.String
		}

		c.HTML(200, "book.html", gin.H{
			"Book":           b,
			"Description":    template.HTML(description.String), // Interpréter le HTML
			"SeriesName":     seriesName.String,
			"SeriesID":       seriesID.Int64,
			"SeriesIndex":    seriesIndex.Float64,
			"BackQuery":      backQuery,
			"BackPage":       backPage,
			"BackSearchMode": backSearchMode,
		})
	})

	// Route Image de couverture (Optimisée avec cache disque)
	r.GET("/cover/:id", func(c *gin.Context) {
		id := c.Param("id")
		path := c.Query("path")

		// Définition du chemin de cache
		cacheDir := filepath.Join(LibraryPath, ".cache", "covers")
		err := os.MkdirAll(cacheDir, 0755)

		cachePath := filepath.Join(cacheDir, id+"_150.jpg")

		// 1. Vérifier si l'image est déjà en cache
		if _, err := os.Stat(cachePath); err == nil {
			c.Set("cache_status", "H")                            // HIT
			c.Header("Cache-Control", "public, max-age=31536000") // 1 an (immuable)
			c.File(cachePath)
			return
		}

		c.Set("cache_status", "M") // MISS par défaut

		// 2. Sinon, redimensionner et mettre en cache
		c.Header("Cache-Control", "public, max-age=604800")
		fullPath := filepath.Join(LibraryPath, path, "cover.jpg")
		file, err := os.Open(fullPath)
		if err != nil {
			c.Set("cache_status", "X")
			c.Status(404)
			return
		}
		defer file.Close()

		img, _, err := image.Decode(file)
		if err != nil {
			c.Set("cache_status", "X")
			c.Status(400) // Image invalide ou corrompue
			return
		}

		// --- LOGIQUE SMART CROP (2:3 Ratio) ---
		bounds := img.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()
		targetRatio := 2.0 / 3.0
		currentRatio := float64(width) / float64(height)

		var startX, startY, endX, endY int
		if currentRatio > targetRatio {
			newWidth := int(float64(height) * targetRatio)
			startX = (width - newWidth) / 2
			endX = startX + newWidth
			endY = height
		} else {
			newHeight := int(float64(width) / targetRatio)
			startY = (height - newHeight) / 2
			endX = width
			endY = startY + newHeight
		}

		rect := image.Rect(startX, startY, endX, endY)
		if sub, ok := img.(SubImager); ok {
			img = sub.SubImage(rect)
		}

		m := resize.Resize(200, 300, img, resize.Lanczos3)

		// Sauvegarder dans le cache
		out, err := os.Create(cachePath)
		if err == nil {
			jpeg.Encode(out, m, &jpeg.Options{Quality: 85})
			out.Close()
			c.Set("cache_status", "C") // OK CACHED
		} else {
			// On continue de servir l'image même si l'écriture en cache échoue
			c.Set("cache_status", "X")
		}

		// Servir l'image
		c.Header("Content-Type", "image/jpeg")
		jpeg.Encode(c.Writer, m, &jpeg.Options{Quality: 85})
	})

	// Route Download
	r.GET("/download/:id", func(c *gin.Context) {
		path := c.Query("path")
		mode := c.Query("mode")
		epubPath := filepath.Join(LibraryPath, path)
		files, _ := os.ReadDir(epubPath)
		var targetFile string

		isKoboMode := mode == "kepub"

		if isKoboMode {
			// Prioritize KEPUB for Kobo Mode
			// 1. Try .kepub.epub
			for _, f := range files {
				if strings.HasSuffix(strings.ToLower(f.Name()), ".kepub.epub") {
					targetFile = filepath.Join(epubPath, f.Name())
					break
				}
			}
			// 2. Try .kepub
			if targetFile == "" {
				for _, f := range files {
					if strings.HasSuffix(strings.ToLower(f.Name()), ".kepub") {
						targetFile = filepath.Join(epubPath, f.Name())
						break
					}
				}
			}
			// 3. Fallback to .epub
			if targetFile == "" {
				for _, f := range files {
					if filepath.Ext(f.Name()) == ".epub" {
						targetFile = filepath.Join(epubPath, f.Name())
						break
					}
				}
			}
		} else {
			// Prioritize EPUB for Standard Mode
			// 1. Try .epub
			for _, f := range files {
				if filepath.Ext(f.Name()) == ".epub" {
					targetFile = filepath.Join(epubPath, f.Name())
					break
				}
			}
			// 2. Fallback to KEPUB variants
			if targetFile == "" {
				for _, f := range files {
					if strings.HasSuffix(strings.ToLower(f.Name()), ".kepub.epub") {
						targetFile = filepath.Join(epubPath, f.Name())
						break
					}
				}
			}
			if targetFile == "" {
				for _, f := range files {
					if strings.HasSuffix(strings.ToLower(f.Name()), ".kepub") {
						targetFile = filepath.Join(epubPath, f.Name())
						break
					}
				}
			}
		}

		if targetFile == "" {
			c.String(404, "Fichier introuvable")
			return
		}

		filename := filepath.Base(targetFile)

		// Kobo Mode Renaming: Ensure it ends in .kepub.epub
		if isKoboMode {
			lowerName := strings.ToLower(filename)
			// Re-evaluating the rename logic based on strict request:
			// "Je veux juste renomer les .kepub en .kepub.epub"
			if strings.HasSuffix(lowerName, ".kepub") {
				filename = filename[:len(filename)-6] + ".kepub.epub"
			}
		}

		c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		c.File(targetFile)
	})

	r.Run(":" + Port)
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
