package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/mmcdole/gofeed"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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
	db.First(&a, ID)
	return a, nil
}

func LoadArticles(db *gorm.DB, includeRead bool, maxArticles int) ([]Article, error) {
	var articles []Article

	if includeRead {
		db.Limit(maxArticles).Find(&articles)
	} else {
		db.Where(&Article{Read: false}).Limit(maxArticles).Find(&articles)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rss, err := fp.ParseURLWithContext(feed.URL, ctx)
	if err != nil {
		return nil, fmt.Errorf("couldnt parse %v: %w", feed.URL, err)
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

func VerboseLog(format string, v ...interface{}) {
	if _verbose {
		log.Printf(format, v...)
	}
}

func indexArticles(db *gorm.DB, index bleve.Index) error {
	articles, err := LoadArticles(db, true, 5000)
	if err != nil {
		return fmt.Errorf("db loading error: %v", err)
	}
	log.Printf("Indexing %d articles from db...", len(articles))

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
	feeds, err := LoadFeeds(db)
	if err != nil {
		return fmt.Errorf("Error loading feeds from db: %v", err)
	}

	log.Printf("Updating %d feeds", len(feeds))

	var wg sync.WaitGroup
	for _, feed := range feeds {
		wg.Add(1)
		go func(feed Feed) {
			newCount := 0
			defer wg.Done()
			// Check for new articles and return a list of articles plus update the db
			newArticles, err := CheckNewArticles(db, &feed)
			if err != nil {
				log.Printf("Error checking new articles: %v", err)
				return
			}

			if len(newArticles) > 0 {
				log.Printf("Retrieved %d articles from %v", len(newArticles), feed.URL)
				//log.Printf("feed %d: %v\n", i, feed)

				// Iterate over the articles and print them
				for _, article := range newArticles {
					article.Feed = feed
					db.Create(&article)
					index.Index(fmt.Sprint(article.ID), article)
					newCount = newCount + 1
				}
			}
		}(feed)
	}
	wg.Wait()
	log.Printf("All checks done")
	return nil
}

func searchArticles(db *gorm.DB, index bleve.Index, args []string) error {
	weightScore := func(score, age float64) float64 {
		la := -math.Log(age)
		recip := 1 / la
		ws := score + recip
		VerboseLog("index score=%.3v, age=%.6v, log(age)=%.5v, f*1/log(age)=%.6v, wscore=%.3v\n", score, age, la, recip, ws)
		return ws
	}
	if len(args) > 0 {
		query := bleve.NewQueryStringQuery(args[0])
		searchRequest := bleve.NewSearchRequest(query) // Eventually pass our scoring into bleve? does it have access to date?
		searchResults, _ := index.Search(searchRequest)
		var sortedResults byWeightedScore
		for _, hit := range searchResults.Hits {
			id, err := strconv.Atoi(hit.ID)
			if err == nil {
				article, err := LoadArticle(db, id)
				if err != nil {
					return fmt.Errorf("error converting search result %v: %w", id, err)
				} else {
					age := time.Since(article.Published).Hours() / 24
					s := scoredArticle{article: article, score: hit.Score, weightedScore: weightScore(hit.Score, age), age: age}
					sortedResults = append(sortedResults, s)

					VerboseLog("score=%.3v, %s, %s\n", hit.Score, article.Published, article.Title)
					VerboseLog("---")
				}
			}
		}
		sort.Sort(byWeightedScore(sortedResults))
		log.Printf("--- Sorted ---")
		for _, sa := range sortedResults {
			//ws := weightScore(sa.score, sa.age)
			//text, err := html2text.FromString(sa.article.Description, html2text.Options{PrettyTables: true})
			//if err != nil {
			//	continue
			//}
			log.Printf("score=%.3v, date=%v, title=%s\n", sa.weightedScore, sa.article.Published, sa.article.Title)
			//log.Printf("\t%s", text)
		}
	} else {
		log.Fatalf("Usage: search <search string>")
	}

	return nil
}

type scoredArticle struct {
	article       *Article
	age           float64
	score         float64
	weightedScore float64
}

type byWeightedScore []scoredArticle

func (a byWeightedScore) Len() int           { return len(a) }
func (a byWeightedScore) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byWeightedScore) Less(i, j int) bool { return a[i].weightedScore > a[j].weightedScore }

var _verbose bool

func main() {

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Set up global flags
	flag.BoolVar(&_verbose, "verbose", false, "print lots of extra info")
	flag.BoolVar(&_verbose, "v", false, "print lots of extra info")

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
		log.Fatal("Please specify a subcommand: search, index, fetch, refresh, prune, unread, import")
	}
	cmd, args := args[0], args[1:]

	switch cmd {
	case "search":
		searchArticles(db, index, args)

	case "index":
		indexArticles(db, index)
	case "fetch":
		updateFeeds(db, index)
	case "refresh":
		updateFeeds(db, index)
		indexArticles(db, index)
	case "prune":
		db.Where("id NOT IN (?)", db.Model(&Article{}).Select("feed_id").Where("feed_id IS NOT NULL")).Delete(&Feed{})
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
