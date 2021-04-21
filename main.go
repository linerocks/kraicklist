package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	dbpath := os.Getenv("DB_PATH")
	db, err := sql.Open("sqlite3", dbpath)
	if err != nil {
		log.Fatalf("Cannot connect to database : %q", err)
		return
	}
	defer db.Close()

	// migration
	migrationOnly := os.Getenv("MIGRATION_ONLY")
	if migrationOnly == "TRUE" {
		// load data to sqlite
		sourcepath := os.Getenv("SOURCE_PATH")
		err := loadData(db, sourcepath, dbpath)
		if err != nil {
			log.Fatalf("unable to start server due: %v", err)
		}
		return
	}

	// define http handlers
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)
	http.HandleFunc("/search", handleSearch(db))

	// define port, we need to set it as env for Heroku deployment
	port := os.Getenv("PORT")
	if port == "" {
		port = "3001"
	}

	// start server
	fmt.Printf("Server is listening on %s...", port)
	err = http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
	if err != nil {
		log.Fatalf("unable to start server due: %v", err)
	}
}

func loadData(db *sql.DB, filepath string, dbpath string) error {
	if _, err := os.Stat(dbpath); os.IsNotExist(err) {
		fmt.Println("Loading data...")

		script := `
			CREATE TABLE records (
				id INTEGER NOT NULL PRIMARY KEY,
				title TEXT,
				content TEXT,
				thumb_url TEXT,
				updated_at INTEGER
			);

			CREATE TABLE tags (
				id INTEGER NOT NULL PRIMARY KEY,
				record_id INTEGER,
				tag TEXT,
				FOREIGN KEY(record_id) REFERENCES records(id)
			);

			CREATE TABLE images (
				id INTEGER NOT NULL PRIMARY KEY,
				record_id INTEGER,
				url TEXT,
				FOREIGN KEY(record_id) REFERENCES records(id)
			);
		`
		_, err = db.Exec(script)
		if err != nil {
			return fmt.Errorf("%q: %s", err, script)
		}

		totalWork := 0
		file, err := os.Open(filepath)
		if err != nil {
			return fmt.Errorf("unable to open source file due: %v", err)
		}
		defer file.Close()
		reader, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("unable to initialize gzip reader due: %v", err)
		}
		cs := bufio.NewScanner(reader)
		for cs.Scan() {
			totalWork++
		}

		currentWork := 0
		file, err = os.Open(filepath)
		if err != nil {
			return fmt.Errorf("unable to open source file due: %v", err)
		}
		defer file.Close()
		reader, err = gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("unable to initialize gzip reader due: %v", err)
		}
		cs = bufio.NewScanner(reader)
		for cs.Scan() {
			var r Record
			err = json.Unmarshal(cs.Bytes(), &r)
			if err != nil {
				continue
			}

			script = fmt.Sprintf(`INSERT INTO records(id, title, content, thumb_url, updated_at) values(
				%d,
				"%s",
				"%s",
				"%s",
				%d
			);
			`,
				r.ID,
				strings.ReplaceAll(r.Title, `"`, `""`),
				strings.ReplaceAll(r.Content, `"`, `""`),
				r.ThumbURL,
				r.UpdatedAt,
			)

			_, err = db.Exec(script)
			if err != nil {
				fmt.Printf("Record insertion script execution error : %v %s", err, script)
				currentWork += 1
				continue
			}

			for _, element := range r.Tags {
				script = fmt.Sprintf(`INSERT INTO tags(record_id, tag) values(
					%d,
					"%s"
				);
				`,
					r.ID,
					element,
				)

				_, err = db.Exec(script)
				if err != nil {
					fmt.Printf("Tags insertion script execution error : %v", err)
					currentWork += 1
					continue
				}
			}

			for _, element := range r.ImageURLs {
				script = fmt.Sprintf(`INSERT INTO images(record_id, url) values(
					%d,
					"%s"
				);
				`,
					r.ID,
					element,
				)

				_, err = db.Exec(script)
				if err != nil {
					fmt.Printf("Images insertion script execution error : %v", err)
					currentWork += 1
					continue
				}
			}

			currentWork += 1
			fmt.Printf("\rProgress %.2f%% (%d/%d)", 100.0*float64(currentWork)/float64(totalWork), currentWork, totalWork)
		}
	}

	fmt.Println("\nDatabase is ready...")
	return nil
}

func handleSearch(db *sql.DB) http.HandlerFunc {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// fetch query string from query params
			q := r.URL.Query().Get("q")
			if len(q) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("missing search query in query params"))
				return
			}

			sizeQuery := r.URL.Query().Get("size")
			size, err := strconv.Atoi(sizeQuery)
			if err != nil {
				size = 10
			}

			cursorQuery := r.URL.Query().Get("cursor")
			cursor, err := strconv.Atoi(cursorQuery)
			if err != nil {
				cursor = 0
			}

			script := fmt.Sprintf(`SELECT COUNT (1) FROM records WHERE title LIKE '%%%s%%' OR content LIKE '%%%s%%'`, q, q)

			var count int
			result, err := db.Query(script)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("query failed"))
				return
			}

			for result.Next() {
				err := result.Scan(&count)
				if err != nil {
					log.Fatal(err)
				}
			}

			start := time.Now()
			script = fmt.Sprintf(`SELECT id, title, content, thumb_url FROM records WHERE (title LIKE '%%%s%%' OR content LIKE '%%%s%%') AND id > %d LIMIT %d`, q, q, cursor, size)

			var records Records
			result, err = db.Query(script)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("query failed"))
				return
			}

			for result.Next() {
				var record Record

				var id int
				var title string
				var content string
				var thumb_url string

				err = result.Scan(&id, &title, &content, &thumb_url)
				if err != nil {
					log.Fatal(err)
				}

				record.ID = id
				record.Title = title
				record.Content = content
				record.ThumbURL = thumb_url

				records = append(records, record)
			}
			elapsed := time.Since(start)

			var remainingItems int
			if len(records) > 0 {
				script = fmt.Sprintf(`SELECT COUNT (1) FROM records WHERE (title LIKE '%%%s%%' OR content LIKE '%%%s%%') AND id > %d`, q, q, records[len(records)-1].ID)
				result, err = db.Query(script)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("query failed"))
					return
				}

				for result.Next() {
					err := result.Scan(&remainingItems)
					if err != nil {
						log.Fatal(err)
					}
				}
			} else {
				remainingItems = 0
			}

			// output success response
			buf := new(bytes.Buffer)
			encoder := json.NewEncoder(buf)
			res := map[string]interface{}{}

			res["docs"] = records
			res["count"] = count
			res["remainingItems"] = remainingItems
			res["size"] = size
			res["cursor"] = cursor
			res["executionTime"] = float64(elapsed) / 10e+6

			if len(records) > 0 {
				res["nextCursor"] = records[len(records)-1].ID
			}

			encoder.Encode(res)
			w.Header().Set("Content-Type", "application/json")
			w.Write(buf.Bytes())
		},
	)
}

type Record struct {
	ID        int      `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	ThumbURL  string   `json:"thumb_url"`
	Tags      []string `json:"tags"`
	UpdatedAt int64    `json:"updated_at"`
	ImageURLs []string `json:"image_urls"`
}

type Records []Record
