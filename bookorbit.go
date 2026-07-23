package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Client BookOrbit (OPDS) -------------------------------------------------
//
// Joseph n'accède plus jamais à une bibliothèque Calibre sur disque : toute la
// donnée (catalogue, couvertures, fichiers) transite par le module OPDS natif
// de BookOrbit (voir /opt/bookorbit/server/src/modules/opds côté serveur).
// L'authentification se fait en HTTP Basic Auth avec un utilisateur OPDS dédié
// (créé via POST /api/v1/opds-users), distinct du compte principal — ça évite
// de stocker le mot de passe du compte web dans ce conteneur et ça n'a pas de
// notion de session/refresh token à gérer côté client.

type opdsClient struct {
	baseURL  string // ex: https://bookorbit.dev.lorien.fr (sans slash final)
	username string
	password string
	http     *http.Client
}

func newOpdsClient(baseURL, username, password string) *opdsClient {
	return &opdsClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Ping vérifie au démarrage que l'URL et les identifiants sont valides. Non
// bloquant si BookOrbit est momentanément injoignable : on log juste un
// avertissement et on continue (il peut redémarrer après nous).
func (c *opdsClient) Ping() error {
	req, err := c.newRequest("GET", "/api/v1/opds", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *opdsClient) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	return req, nil
}

// --- Modèle Atom/OPDS ---------------------------------------------------
// BookOrbit sert des flux Atom (application/atom+xml;profile=opds-catalog).
// On ne déclare que les éléments qui nous intéressent ; encoding/xml ignore
// silencieusement le reste. Les préfixes de namespace (dc:, opensearch:) sont
// transparents pour le matching par nom local.

type atomFeed struct {
	XMLName      xml.Name    `xml:"feed"`
	TotalResults int         `xml:"totalResults"`
	Entries      []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID      string       `xml:"id"`
	Title   string       `xml:"title"`
	Authors []atomAuthor `xml:"author"`
	Content string       `xml:"content"`
	Links   []atomLink   `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

const (
	relSeries      = "http://opds-spec.org/sort/series"
	relAcquisition = "http://opds-spec.org/acquisition"
)

// toBook convertit une entrée Atom OPDS en Book utilisable par les templates.
func (e atomEntry) toBook() Book {
	b := Book{Title: e.Title, Description: e.Content}

	if id := parseTrailingID(e.ID); id > 0 {
		b.ID = id
	}

	names := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) > 0 {
		b.PrimaryAuthor = names[0]
		b.Author = strings.Join(names, " & ")
	}

	for _, l := range e.Links {
		switch l.Rel {
		case relSeries:
			b.SeriesID = parseSeriesIDFromHref(l.Href)
			b.Series, b.SeriesIndex = parseSeriesTitle(l.Title)
		case relAcquisition:
			fileID := parseFileIDFromHref(l.Href)
			format := strings.ToLower(l.Title)
			b.Files = append(b.Files, BookFile{ID: fileID, Format: format})
			if format == "kepub" {
				b.HasKepub = true
			}
		}
	}

	return b
}

// parseTrailingID extrait l'entier final de "urn:bookorbit:book:123".
func parseTrailingID(urn string) int {
	idx := strings.LastIndex(urn, ":")
	if idx == -1 {
		return 0
	}
	n, _ := strconv.Atoi(urn[idx+1:])
	return n
}

func parseSeriesIDFromHref(href string) int {
	u, err := url.Parse(href)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(u.Query().Get("seriesId"))
	return n
}

func parseFileIDFromHref(href string) int {
	u, err := url.Parse(href)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(u.Query().Get("fileId"))
	return n
}

// parseSeriesTitle sépare "Nom de la série #2.5" en ("Nom de la série", 2.5).
// L'index est optionnel (BookOrbit ne l'ajoute que s'il est connu).
func parseSeriesTitle(title string) (string, float64) {
	if idx := strings.LastIndex(title, " #"); idx != -1 {
		if n, err := strconv.ParseFloat(title[idx+2:], 64); err == nil {
			return title[:idx], n
		}
	}
	return title, 0
}

// catalogParams construit les query params pour GET /api/v1/opds/catalog.
type catalogParams struct {
	Page     int
	Size     int
	Query    string
	Author   string
	SeriesID int
}

func (c *opdsClient) fetchCatalog(p catalogParams) ([]Book, int, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(p.Page))
	q.Set("size", strconv.Itoa(p.Size))
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.Author != "" {
		q.Set("author", p.Author)
	}
	if p.SeriesID > 0 {
		q.Set("seriesId", strconv.Itoa(p.SeriesID))
	}

	req, err := c.newRequest("GET", "/api/v1/opds/catalog?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("bookorbit: HTTP %d sur /opds/catalog", resp.StatusCode)
	}

	var feed atomFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, 0, fmt.Errorf("bookorbit: flux OPDS illisible: %w", err)
	}

	books := make([]Book, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		books = append(books, e.toBook())
	}
	return books, feed.TotalResults, nil
}

// proxyGet relaie une requête GET authentifiée vers BookOrbit et retourne la
// réponse brute (l'appelant est responsable de fermer resp.Body).
func (c *opdsClient) proxyGet(path string, extraHeaders http.Header) (*http.Response, error) {
	req, err := c.newRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	for k, values := range extraHeaders {
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	return c.http.Do(req)
}

// --- Cache mémoire des livres ------------------------------------------
//
// L'OPDS de BookOrbit n'expose pas de route "un livre par id" — seulement des
// flux de catalogue paginés/filtrés. La page de détail et le téléchargement
// s'appuient donc sur le cache rempli par la dernière page de catalogue
// affichée (chaque entrée OPDS transporte déjà tout le détail : description,
// série, fichiers). En usage normal (parcourir la liste → cliquer un livre),
// l'entrée est toujours déjà en cache. C'est un compromis assumé : voir le
// README pour le cas limite (accès direct à /book/:id sans être passé par la
// liste, après expiration du cache).

type bookCache struct {
	mu       sync.Mutex
	entries  map[int]Book
	order    []int // ordre d'insertion, pour l'éviction FIFO
	capacity int
}

func newBookCache(capacity int) *bookCache {
	return &bookCache{entries: make(map[int]Book), capacity: capacity}
}

func (c *bookCache) PutAll(books []Book) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range books {
		if _, exists := c.entries[b.ID]; !exists {
			c.order = append(c.order, b.ID)
		}
		c.entries[b.ID] = b
	}
	for len(c.order) > c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *bookCache) Get(id int) (Book, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.entries[id]
	return b, ok
}
