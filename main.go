package main

import (
	"bufio"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/blevesearch/bleve"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
)

type Article struct {
	ID          int       `json:"id"`
	Read        bool      `json:"read"`
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Fetched     time.Time `json:"fetched"`
	FeedID      int       `json:"feed"`
}

func (a *Article) String() string {
	return fmt.Sprintf("%v: %s", a.Published, a.Title)
}

// Feed struct
type Feed struct {
	ID              int       `json:"id"`
	URL             string    `json:"url"`
	LastCheckedTime time.Time `json:"last_checked_time"`
}

// Function to create the Feeds table
func CreateFeedsTable(db *sql.DB) {
	sql_table := `
	CREATE TABLE IF NOT EXISTS Feeds(
		ID INTEGER PRIMARY KEY AUTOINCREMENT,
		URL TEXT NOT NULL UNIQUE,
		LastCheckedTime TIMESTAMP);
	`

	_, err := db.Exec(sql_table)
	if err != nil {
		log.Fatalf("Failed to create Feeds table: %v", err)
	}
}

func LoadArticle(db *sql.DB, ID int) (*Article, error) {
	query := "SELECT * FROM Articles WHERE ID == ? LIMIT 1;"
	rows, err := db.Query(query, ID)
	if err != nil {
		return nil, fmt.Errorf("db query error: %w")
	}
	defer rows.Close()

	for rows.Next() {
		var a Article
		if err := rows.Scan(&a.ID, &a.FeedID, &a.Read, &a.Title, &a.Link, &a.Description, &a.Published, &a.Fetched); err != nil {
			return nil, fmt.Errorf("Error parsing row: %w", err)
		}
		return &a, nil
	}

	return nil, errors.New("no matching article found")
}

func LoadArticles(db *sql.DB, includeRead bool, maxArticles int) ([]Article, error) {
	var articles []Article
	var query string
	if includeRead {
		query = "SELECT * FROM Articles LIMIT ?;"
	} else {
		query = "SELECT * FROM Articles WHERE Read==0 LIMIT ?;"
	}

	rows, err := db.Query(query, maxArticles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a Article
		if err := rows.Scan(&a.ID, &a.FeedID, &a.Read, &a.Title, &a.Link, &a.Description, &a.Published, &a.Fetched); err != nil {
			return nil, fmt.Errorf("Error parsing row: %v", err)
		}
		articles = append(articles, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return articles, nil
}

// Load a feed
func LoadFeeds(db *sql.DB) ([]Feed, error) {
	var feeds []Feed
	rows, err := db.Query("SELECT * FROM Feeds;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var feed Feed
		if err := rows.Scan(&feed.ID, &feed.URL, &feed.LastCheckedTime); err != nil {
			return nil, fmt.Errorf("LoadFeeds %v", err)
		}
		feeds = append(feeds, feed)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("LoadFeeds %v", err)
	}
	return feeds, nil
}

// Function to store a Feed
func StoreFeed(db *sql.DB, feed Feed) error {
	sql_addfeed := `
	INSERT INTO Feeds(
		URL,
		LastCheckedTime)
	VALUES(?, ?);
	`
	sql_updatefeed := `
	REPLACE INTO Feeds(
		ID,
		URL,
		LastCheckedTime)
	VALUES(?, ?, ?);
	`

	var creating bool
	var sql string
	if feed.ID == 0 {
		creating = true
		sql = sql_addfeed
	} else {
		creating = false
		sql = sql_updatefeed
	}

	stmt, err := db.Prepare(sql)
	if err != nil {
		return err
	}
	defer stmt.Close()

	if creating == true {
		_, err = stmt.Exec(feed.URL, feed.LastCheckedTime)
	} else {
		_, err = stmt.Exec(feed.ID, feed.URL, feed.LastCheckedTime)
	}
	if err != nil {
		return err
	}

	return nil
}

func InitDB(filepath string) *sql.DB {
	db, err := sql.Open("sqlite3", filepath)
	if err != nil {
		log.Fatal(err)
	}
	if db == nil {
		log.Fatal("db nil")
	}

	CreateArticleTable(db)
	CreateFeedsTable(db)

	return db
}

func CreateArticleTable(db *sql.DB) {
	// Create table if it doesn't exist
	sql_table := `
	CREATE TABLE IF NOT EXISTS Articles(
		ID INTEGER PRIMARY KEY AUTOINCREMENT,
        FeedID INTEGER,
		Read BOOLEAN,
		Title TEXT,
		Link TEXT,
		Description TEXT,
		Published TIMESTAMP,
		Fetched TIMESTAMP,
        FOREIGN KEY(FeedID) REFERENCES Feeds(ID));
	`

	_, err := db.Exec(sql_table)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}
}

func StoreArticle(db *sql.DB, article Article, FeedID int) error {
	sql_additem := `
	INSERT INTO Articles(
        FeedID,
		Read,
		Title,
		Link,
		Description,
		Published,
		Fetched)
	VALUES(?, ?, ?, ?, ?, ?, ?);
	`
	stmt, err := db.Prepare(sql_additem)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(FeedID, article.Read, article.Title, article.Link, article.Description, article.Published, article.Fetched)
	if err != nil {
		return err
	}

	return nil
}

// Function to update the LastCheckedTime of a Feed
func UpdateFeedLastCheckedTime(db *sql.DB, feed *Feed) error {
	sql_update := `
	UPDATE Feeds
	SET LastCheckedTime = ?
	WHERE ID = ?;
	`
	stmt, err := db.Prepare(sql_update)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(feed.LastCheckedTime, feed.ID)
	if err != nil {
		return err
	}

	return nil
}

func attemptTimeParse(formats []string, input string) (time.Time, error) {
	var parsedTime time.Time
	var err error

	for _, format := range formats {
		parsedTime, err = time.Parse(format, input)
		if err == nil {
			// Parsing succeeded, return the parsed time
			return parsedTime, nil
		}
	}

	// If no format successfully parses the input, return an error
	return time.Time{}, fmt.Errorf("failed to parse date/time : %w", err)
}

func CheckNewArticles(db *sql.DB, feed *Feed) ([]Article, error) {
	dateFormats := []string{
		time.RFC1123,
		time.RFC3339,
		time.RFC1123Z,
		"Mon, 2 Jan 2006 15:04:05 -0700", // RFC1123 with numeric zone and no leading zero on day
	}

	//log.Printf("Last check time: %v", feed.LastCheckedTime)
	checkTime := time.Now()
	fp := gofeed.NewParser()
	rss, err := fp.ParseURL(feed.URL)
	if err != nil {
		return nil, err
	}

	var articles []Article
	for _, item := range rss.Items {
		pubDate, err := attemptTimeParse(dateFormats, item.Published)
		if err != nil {
			log.Printf("Error parsing date (%v): %s", item.Published, err)
			continue
		}
		if pubDate.After(feed.LastCheckedTime) {
			//	log.Printf("New article found: feedID=%d pubDate=%v title=%s", feed.ID, pubDate, item.Title)
			articles = append(articles, Article{
				Title:       item.Title,
				Read:        false,
				Link:        item.Link,
				Description: item.Description,
				Published:   pubDate,
				Fetched:     checkTime,
			})
		}
	}
	feed.LastCheckedTime = checkTime
	UpdateFeedLastCheckedTime(db, feed)
	return articles, nil
}

func CreateFeeds(path string, db *sql.DB) error {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		url := scanner.Text()
		//log.Printf("Creating new feed from %s", url)
		feed := &Feed{
			URL:             url,
			LastCheckedTime: time.Time{}, // initialize to zero value
		}

		err := StoreFeed(db, *feed)
		if err != nil {
			log.Printf("WARN: Failed to initialize feed (%s): %v", url, err)
		}
	}
	return nil
}

var (
	_verbose = flag.Bool("verbose", false, "print lots of extra info")
)

func markAllRead(db *sql.DB) error {
	_, err := db.Query("UPDATE Articles SET Read=1;")
	if err != nil {
		return err
	}
	return nil
}

func indexArticles(db *sql.DB, index bleve.Index) error {
	articles, err := LoadArticles(db, true, 500)
	if err != nil {
		return fmt.Errorf("db loading error: %v", err)
	}
	log.Printf("Loaded %d articles from db", len(articles))

	for _, a := range articles {
		index.Index(fmt.Sprintf("%d", a.ID), a) // TODO: is there no way to make the key an int?
	}
	return nil
}

func printArticles(db *sql.DB, printRead bool) error {
	articles, err := LoadArticles(db, printRead, 500)
	if err != nil {
		log.Printf("Error loading articles from db: %+v", err)
		return err
	}
	log.Printf("Loaded %d articles", len(articles))

	for _, a := range articles {
		log.Printf("%+v", a)
		//fmt.Printf("Title: %s\n", a.Title)
		//fmt.Printf("Link: %s\n", a.Link)
		////		fmt.Printf("Description: %s\n", article.Description)
		//fmt.Printf("Published: %s\n", a.Published)
		fmt.Println()
	}
	return nil
}

func pruneFeeds(db *sql.DB) error {
	query := "DELETE FROM Feeds WHERE ID NOT IN ( SELECT FeedID FROM Articles WHERE FeedID IS NOT NULL);"
	result, err := db.Exec(query)
	if err != nil {
		return fmt.Errorf("pruning empty feeds: %w", err)
	}
	rows, err := result.RowsAffected()
	if err == nil {
		log.Printf("%d feeds pruned", rows)
	}
	return nil
}

func updateFeeds(db *sql.DB, index bleve.Index) error {
	newCount := 0
	feeds, err := LoadFeeds(db)
	if err != nil {
		return fmt.Errorf("Error loading feeds from db: %v", err)
	}

	log.Printf("Updating %d feeds", len(feeds))

	for i, feed := range feeds {
		// Check for new articles and return a list of articles plus update the db
		newArticles, err := CheckNewArticles(db, &feed)
		if err != nil {
			log.Printf("Error checking new articles: %v", err)
			continue
		}

		if len(newArticles) > 0 {
			log.Printf("Retrieved %d articles from %v", len(newArticles), feed.URL)
			log.Printf("feed %d: %v\n", i, feed)

			// Iterate over the articles and print them
			for _, article := range newArticles {
				err = StoreArticle(db, article, feed.ID)
				if err != nil {
					return fmt.Errorf("Failed to store article: %v", err)
				}
				index.Index(string(article.ID), article)
				newCount = newCount + 1
			}
		}
	}
	log.Printf("Fetched %d new articles", newCount)
	return nil
}

func main() {

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Parse global flags
	flag.Parse()

	db := InitDB("rss.db")
	defer db.Close()
	//log.Printf("Database ready")

	var err error
	var index bleve.Index

	index, err = bleve.Open("_bleve")
	if err == bleve.ErrorIndexPathDoesNotExist {
		mapping := bleve.NewIndexMapping()
		index, err = bleve.New("_bleve", mapping)
		if err != nil {
			log.Fatalf("index creation error: %v", err)
		}
	} else if err != nil {
		log.Fatalf("index open error: %v", err)
	}

	// Parse subcommand
	args := flag.Args()
	if len(args) == 0 {
		log.Fatal("Please specify a subcommand.")
	}
	cmd, args := args[0], args[1:]

	switch cmd {
	case "search":
		if len(args) > 0 {
			query := bleve.NewQueryStringQuery(args[0])
			searchRequest := bleve.NewSearchRequest(query)
			searchResults, _ := index.Search(searchRequest)
			for _, hit := range searchResults.Hits {
				id, err := strconv.Atoi(hit.ID)
				if err == nil {
					article, err := LoadArticle(db, id)
					if err != nil {
						log.Printf("error converting search result %s: %w", id, err)
						break
					} else {
						log.Printf("score=%.3v, %s, %s\n", hit.Score, article.Published, article.Title)
					}

				}
			}
		} else {
			log.Fatalf("Usage: search <search string>")
		}

	case "index":
		indexArticles(db, index)
	case "update":
		updateFeeds(db, index)
	case "prune":
		err := pruneFeeds(db)
		if err != nil {
			log.Fatalf("error: %w", err)
		}
	case "unread":
		err := printArticles(db, false)
		if err != nil {
			log.Fatalf("Error reading unread article: %v", err)
		}
		markAllRead(db)
		if err != nil {
			log.Fatalf("Error setting unread flag: %v", err)
		}

	case "import":
		if len(args) > 0 {
			err := CreateFeeds(args[0], db)
			if err != nil {
				log.Fatalf("Error adding feeds to db: %v", err)
			}
		} else {
			log.Fatalf("Usage: add <filename>")
		}
	default:
		log.Fatalf("Unrecognized command %q. "+
			"Command must be one of: update, unread", cmd)
	}

}
