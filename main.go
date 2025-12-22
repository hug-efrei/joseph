package main

import (
	"database/sql"
	"html/template"
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nfnt/resize"
)

// Configuration par défaut
var (
	LibraryPath  = getEnv("LIBRARY_PATH", "/books")
	Port         = getEnv("PORT", "8080")
	BooksPerPage = 6
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

func main() {
	LibraryPath = getEnv("LIBRARY_PATH", "/books")
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
	r := gin.Default()

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

		page, _ := strconv.Atoi(pageStr)
		if page < 1 {
			page = 1
		}
		offset := (page - 1) * BooksPerPage

		// Requête de base
		baseQuery := `
			SELECT 
				b.id, b.title, a.name, a.id, b.path, s.name, s.id, b.series_index,
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

		// TRI INTELLIGENT :
		// Si on regarde une série, on veut l'ordre 1, 2, 3...
		// Sinon on veut les derniers ajouts.
		if seriesID != "" {
			baseQuery += " ORDER BY b.series_index ASC"
		} else {
			baseQuery += " ORDER BY b.id DESC"
		}

		baseQuery += " LIMIT ? OFFSET ?"
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
		if len(books) > BooksPerPage {
			hasNext = true
			books = books[:BooksPerPage]
		}

		// On passe les filtres actuels au template pour la pagination
		c.HTML(200, "index.html", gin.H{
			"Books": books, "Query": query,
			"AuthorID": authorID, "SeriesID": seriesID,
			"Page": page, "HasNext": hasNext, "PrevPage": page - 1, "NextPage": page + 1,
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

		if seriesName.Valid {
			b.Series = seriesName.String
			b.SeriesID = int(seriesID.Int64)
			b.SeriesIndex = seriesIndex.Float64
		}
		if description.Valid {
			b.Description = description.String
		}

		c.HTML(200, "book.html", gin.H{"Book": b})
	})

	r.GET("/cover/:id", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=604800")

		path := c.Query("path")
		fullPath := filepath.Join(LibraryPath, path, "cover.jpg")
		file, err := os.Open(fullPath)
		if err != nil {
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
