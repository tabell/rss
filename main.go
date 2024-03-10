package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/mmcdole/gofeed"
    "gorm.io/gorm"
    "gorm.io/driver/sqlite"

)

type Article struct {
    gorm.Model
	Read        bool      `json:"read"`
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Description string    `json:"description"`
	Published   time.Time `json:"published"`
	Fetched     time.Time `json:"fetched"`
	FeedID      int       `json:"feed"`
    Feed        Feed
}

func (a *Article) String() string {
	return fmt.Sprintf("%v: %s", a.Published, a.Title)
}

// Feed struct
type Feed struct {
    gorm.Model
	URL             string    `json:"url"`
	LastCheckedTime time.Time `json:"last_checked_time"`
}

func LoadArticle(db *gorm.DB, ID int) (a *Article, err error) {
    db.First(&a, 10)
    return a, nil
}

func LoadArticles(db *gorm.DB, includeRead bool, maxArticles int) ([]Article, error) {
	var articles []Article

	if includeRead {
        db.Limit(maxArticles).Find(&articles)
	} else {
        db.Where(&Article{Read:false}).Limit(maxArticles).Find(&articles)
	}
	return articles, nil
}

// Load a feed
func LoadFeeds(db *gorm.DB) ([]Feed, error) {
	var feeds []Feed
    db.Find(&feeds)

	return feeds, nil
}

func InitDB(filepath string) *gorm.DB {
    db, err := gorm.Open(sqlite.Open(filepath), &gorm.Config{})

	if err != nil {

		log.Fatal(err)
	}
	if db == nil {
		log.Fatal("db nil")
	}

    db.AutoMigrate(&Article{}, &Feed{})

	return db
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

func CheckNewArticles(db *gorm.DB, feed *Feed) ([]Article, error) {
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
    db.Save(&feed) // TODO: wasteful to save everything
	return articles, nil
}

func CreateFeeds(path string, db *gorm.DB) error {
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

        db.Create(&feed)
	}
	return nil
}

var (
	_verbose = flag.Bool("verbose", false, "print lots of extra info")
)

func indexArticles(db *gorm.DB, index bleve.Index) error {
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

func printArticles(db *gorm.DB, printRead bool) error {
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

func updateFeeds(db *gorm.DB, index bleve.Index) error {
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
                article.Feed = feed
                db.Create(&article)
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
        db.Where("id NOT IN (?)", db.Model(&Article{}).Select("FeedID").Where("FeedID IS NOT NULL")).Delete(&Feed{})
	case "unread":
		err := printArticles(db, false)
		if err != nil {
			log.Fatalf("Error reading unread article: %v", err)
		}
        db.Model(&Article{}).Update("Read", 1)
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
