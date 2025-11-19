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

const LibraryPath = "/books"
const BooksPerPage = 10

type Book struct {
	ID     int
	Title  string
	Author string
	Path   string
}

func main() {
	dbPath := filepath.Join(LibraryPath, "metadata.db")
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		log.Fatal("Impossible d'ouvrir la DB:", err)
	}
	defer db.Close()

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

		baseQuery := `
			SELECT b.id, b.title, a.name, b.path
			FROM books b
			JOIN books_authors_link bal ON b.id = bal.book
			JOIN authors a ON bal.author = a.id
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
			rows.Scan(&b.ID, &b.Title, &b.Author, &b.Path)
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

		for _, f := range files {
			if filepath.Ext(f.Name()) == ".kepub.epub" {
				targetFile = filepath.Join(epubPath, f.Name())
				break
			}
		}
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
		c.Header("Content-Disposition", "attachment; filename=\""+filepath.Base(targetFile)+"\"")
		c.File(targetFile)
	})

	r.Run(":8080")
}
