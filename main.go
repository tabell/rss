package main

// Downloads articles and stores Feed + articles in DB

// Next goal is to make it only add articles if new. On opening app, should check the db and repopulate data structures
// Then support multiple feeds, and a way to sort all articles
// by date, and display the top N articles
// Then add graphics. Split into two panes vertically. Left pane is a list of articles
// Right pane is a preview of "selected" article (selected article automatically cycles through every 10s)

// Currently running the main function will read existing feeds out of the database, download all new articles and store them back to db
// Does not currently check for dupes
import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
)

type Article struct {
	Unread      bool      `json:"unread"`
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Fetched     time.Time `json:"fetched"`
	FeedID      int       `json:"feed"`
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

func LoadUnreadArticles(db *sql.DB) ([]Article, error) {
	var articles []Article
	rows, err := db.Query("SELECT * FROM Articles WHERE Unread==1;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a Article
		if err := rows.Scan(&a.FeedID, &a.Unread, &a.Title, &a.Link, &a.Description, &a.Published, &a.Fetched); err != nil {
			return nil, fmt.Errorf("%v", err)
		}
		articles = append(articles, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%v", err)
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
        FeedID INTEGER,
		Unread BOOLEAN,
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
		Unread,
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

	_, err = stmt.Exec(FeedID, article.Unread, article.Title, article.Link, article.Description, article.Published, article.Fetched)
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
	return time.Time{}, fmt.Errorf("failed to parse input '%s'", input)
}

func CheckNewArticles(db *sql.DB, feed *Feed) ([]Article, error) {
	dateFormats := []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC3339,
	}
	log.Printf("Last check time: %v", feed.LastCheckedTime)
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
			log.Printf("%+v", item)
			log.Fatalf("Error parsing date: %v", err)
		}
		//if pubDate.After(feed.LastCheckedTime.Add(-time.Hour * 24)) {
		if pubDate.After(feed.LastCheckedTime) {
			log.Printf("New article found: feedID=%d pubDate=%v title=%s", feed.ID, pubDate, item.Title)
			articles = append(articles, Article{
				Title:       item.Title,
				Unread:      true,
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
		log.Printf("Creating new feed from %s", url)
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
	_, err := db.Query("UPDATE Articles SET Unread=0;")
	if err != nil {
		return err
	}
	return nil
}

func printUnread(db *sql.DB) error {
	articles, err := LoadUnreadArticles(db)
	if err != nil {
		return fmt.Errorf("Error loading articles from db: %+v", err)
	}

	for _, a := range articles {
		fmt.Printf("Title: %s\n", a.Title)
		fmt.Printf("Link: %s\n", a.Link)
		//		fmt.Printf("Description: %s\n", article.Description)
		fmt.Printf("Published: %s\n", a.Published)
		fmt.Println()
	}
	return nil
}
func updateFeeds(db *sql.DB) error {
	feeds, err := LoadFeeds(db)
	if err != nil {
		return fmt.Errorf("Error loading feeds from db: %v", err)
	}

	log.Printf("Updating %d feeds", len(feeds))

	for i, f := range feeds {
		log.Printf("feed %d: %v\n", i, f)
		// Check for new articles and return a list of articles plus update the db
		newArticles, err := CheckNewArticles(db, &f)
		if err != nil {
			log.Printf("Error checking new articles: %v", err)
			continue
		}

		log.Printf("Retrieved %d articles", len(newArticles))

		// Iterate over the articles and print them
		for _, article := range newArticles {
			err = StoreArticle(db, article, f.ID)
			if err != nil {
				return fmt.Errorf("Failed to store article: %v", err)
			}
		}
	}
	return nil
}

func main() {

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Parse global flags
	flag.Parse()

	db := InitDB("rss.db")
	defer db.Close()
	log.Printf("Database ready")

	// Parse subcommand
	args := flag.Args()
	if len(args) == 0 {
		log.Fatal("Please specify a subcommand.")
	}
	cmd, args := args[0], args[1:]

	switch cmd {
	case "update":
		updateFeeds(db)
	case "unread":
		err := printUnread(db)
		if err != nil {
			log.Fatalf("Error reading unread article: %v", err)
		}
		markAllRead(db)
		if err != nil {
			log.Fatalf("Error setting unread flag: %v", err)
		}

	case "add":
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
