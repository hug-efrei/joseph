package main

import (
	"database/sql"
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nfnt/resize"
)

// Configuration par défaut (surchargeable par ENV)
var (
	LibraryPath  = getEnv("LIBRARY_PATH", "/books")
	Port         = getEnv("PORT", "8080")
	BooksPerPage = 10
)

type Book struct {
	ID          int
	Title       string
	Author      string
	Path        string
	Series      string  // Nouveau
	SeriesIndex float64 // Nouveau (ex: 1.0, 1.5)
}

func main() {
	// Recupération dynamique de la config
	LibraryPath = getEnv("LIBRARY_PATH", "/books")

	dbPath := filepath.Join(LibraryPath, "metadata.db")
	// Vérification basique
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Fatalf("Erreur : Aucune base de données trouvée à %s. Vérifiez votre volume Docker.", dbPath)
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		log.Fatal("Impossible d'ouvrir la DB:", err)
	}
	defer db.Close()

	// Mode Release pour la prod (moins de logs, plus rapide)
	gin.SetMode(gin.ReleaseMode)

	r := gin.Default()
	r.LoadHTMLGlob("templates/*")

	r.GET("/", func(c *gin.Context) {
		query := c.Query("q")
		pageStr := c.Query("page")

		page, _ := strconv.Atoi(pageStr)
		if page < 1 {
			page = 1
		}
		offset := (page - 1) * BooksPerPage

		// Requête SQL enrichie pour les Séries
		// On utilise LEFT JOIN car un livre n'a pas forcément de série
		baseQuery := `
			SELECT 
				b.id, 
				b.title, 
				a.name, 
				b.path,
				s.name,
				b.series_index
			FROM books b
			JOIN books_authors_link bal ON b.id = bal.book
			JOIN authors a ON bal.author = a.id
			LEFT JOIN books_series_link bsl ON b.id = bsl.book
			LEFT JOIN series s ON bsl.series = s.id
			WHERE b.id IN (SELECT book FROM data WHERE format = 'EPUB' OR format = 'KEPUB')
		`

		args := []interface{}{}
		if query != "" {
			baseQuery += " AND (b.title LIKE ? OR a.name LIKE ?)"
			args = append(args, "%"+query+"%", "%"+query+"%")
		}

		baseQuery += " ORDER BY b.id DESC LIMIT ? OFFSET ?"
		args = append(args, BooksPerPage+1, offset)

		rows, err := db.Query(baseQuery, args...)
		if err != nil {
			c.String(500, "Erreur DB: "+err.Error())
			return
		}
		defer rows.Close()

		var books []Book
		for rows.Next() {
			var b Book
			var seriesName sql.NullString   // Gère le NULL si pas de série
			var seriesIndex sql.NullFloat64 // Gère le NULL

			err := rows.Scan(&b.ID, &b.Title, &b.Author, &b.Path, &seriesName, &seriesIndex)
			if err != nil {
				continue
			}

			if seriesName.Valid {
				b.Series = seriesName.String
				b.SeriesIndex = seriesIndex.Float64
			}

			books = append(books, b)
		}

		hasNext := false
		if len(books) > BooksPerPage {
			hasNext = true
			books = books[:BooksPerPage]
		}

		c.HTML(200, "index.html", gin.H{
			"Books":    books,
			"Query":    query,
			"Page":     page,
			"HasNext":  hasNext,
			"PrevPage": page - 1,
			"NextPage": page + 1,
		})
	})

	r.GET("/cover/:id", func(c *gin.Context) {
		path := c.Query("path")
		fullPath := filepath.Join(LibraryPath, path, "cover.jpg")
		file, err := os.Open(fullPath)
		if err != nil {
			// Image placeholder transparente 1x1 si pas de cover (évite une croix rouge moche)
			c.Status(404)
			return
		}
		defer file.Close()

		img, _, err := image.Decode(file)
		if err == nil {
			m := resize.Resize(150, 0, img, resize.Bilinear)
			c.Header("Content-Type", "image/jpeg")
			opts := jpeg.Options{Quality: 60}
			jpeg.Encode(c.Writer, m, &opts)
		}
	})

	r.GET("/download/:id", func(c *gin.Context) {
		path := c.Query("path")
		epubPath := filepath.Join(LibraryPath, path)
		files, _ := os.ReadDir(epubPath)
		var targetFile string

		// Priorité KEPUB
		for _, f := range files {
			if filepath.Ext(f.Name()) == ".kepub.epub" {
				targetFile = filepath.Join(epubPath, f.Name())
				break
			}
		}
		// Fallback EPUB
		if targetFile == "" {
			for _, f := range files {
				if filepath.Ext(f.Name()) == ".epub" {
					targetFile = filepath.Join(epubPath, f.Name())
					break
				}
			}
		}

		if targetFile == "" {
			c.String(404, "Fichier introuvable")
			return
		}

		fileName := filepath.Base(targetFile)
		c.Header("Content-Disposition", "attachment; filename=\""+fileName+"\"")
		c.File(targetFile)
	})

	log.Printf("Joseph démarré sur le port %s avec la librairie : %s", Port, LibraryPath)
	r.Run(":" + Port)
}

// Helper pour lire les variables d'environnement
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
