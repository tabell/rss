package main

// Downloads articles and stores Feed + articles in DB

// Next goal is to make it only add articles if new. On opening app, should check the db and repopulate data structures
// Then support multiple feeds, and a way to sort all articles
// by date, and display the top N articles
// Then add graphics. Split into two panes vertically. Left pane is a list of articles
// Right pane is a preview of "selected" article (selected article automatically cycles through every 10s)

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
)

type Article struct {
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Fetched     time.Time `json:"fetched"`
}

// RSSFeed struct
type RSSFeed struct {
	ID              int       `json:"id"`
	URL             string    `json:"url"`
	LastCheckedTime time.Time `json:"last_checked_time"`
}

// Function to create the Feeds table
func CreateFeedsTable(db *sql.DB) {
	sql_table := `
	CREATE TABLE IF NOT EXISTS Feeds(
		ID INTEGER PRIMARY KEY,
		URL TEXT NOT NULL UNIQUE,
		LastCheckedTime TIMESTAMP);
	`

	_, err := db.Exec(sql_table)
	if err != nil {
		log.Fatalf("Failed to create Feeds table: %v", err)
	}
}

// Function to store a RSSFeed
func StoreFeed(db *sql.DB, feed RSSFeed) error {
	sql_addfeed := `
	INSERT OR REPLACE INTO Feeds(
		ID,
		URL,
		LastCheckedTime)
	VALUES(?, ?, ?);
	`
	stmt, err := db.Prepare(sql_addfeed)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(feed.ID, feed.URL, feed.LastCheckedTime)
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
		Title,
		Link,
		Description,
		Published,
		Fetched)
	VALUES(?, ?, ?, ?, ?, ?);
	`
	stmt, err := db.Prepare(sql_additem)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(FeedID, article.Title, article.Link, article.Description, article.Published, article.Fetched)
	if err != nil {
		return err
	}

	return nil
}

// Function to update the LastCheckedTime of a RSSFeed
func UpdateFeedLastCheckedTime(db *sql.DB, feed *RSSFeed) error {
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

func CheckNewArticles(db *sql.DB, feed *RSSFeed) ([]Article, error) {
	checkTime := time.Now()
	fp := gofeed.NewParser()
	rss, err := fp.ParseURL(feed.URL)
	if err != nil {
		return nil, err
	}

	var articles []Article
	for _, item := range rss.Items {
		pubDate, err := time.Parse("Mon, 02 Jan 2006 15:04:05 MST", item.Published)
		if err != nil {
			continue
		}
		if pubDate.After(feed.LastCheckedTime) {
			articles = append(articles, Article{
				Title:       item.Title,
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

func main() {

	db := InitDB("rss.db")
	log.Printf("Database ready")
	// Create a RSSFeed
	// feed := &RSSFeed{
	// 	URL:             "https://therecord.media/feed/",
	// 	LastCheckedTime: time.Now().Add(-24 * time.Hour), // Last checked 24 hours ago
	// }

	err := StoreFeed(db, *feed)
	if err != nil {
		log.Fatalf("Failed to store feed: %v", err)
	}

	// Check for new articles and return a list of articles plus update the db
	newArticles, err := CheckNewArticles(db, feed)
	if err != nil {
		log.Fatalf("Error checking new articles: %v", err)
	}

	// Iterate over the articles and print them
	for _, article := range newArticles {
		err = StoreArticle(db, article, feed.ID)
		if err != nil {
			log.Fatalf("Failed to store article: %v", err)
		}
		fmt.Printf("Title: %s\n", article.Title)
		fmt.Printf("Link: %s\n", article.Link)
		//		fmt.Printf("Description: %s\n", article.Description)
		fmt.Printf("Published: %s\n", article.Published)
		fmt.Println()
	}
}
