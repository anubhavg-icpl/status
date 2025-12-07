package feeds

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/status/storage"
)

// RSSFeed generates RSS 2.0 feed
type RSSFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Generator     string    `xml:"generator"`
	Items         []RSSItem `xml:"item"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
	Category    string `xml:"category,omitempty"`
}

// AtomFeed generates Atom 1.0 feed
type AtomFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	Xmlns     string      `xml:"xmlns,attr"`
	Title     string      `xml:"title"`
	Subtitle  string      `xml:"subtitle,omitempty"`
	Link      []AtomLink  `xml:"link"`
	Updated   string      `xml:"updated"`
	ID        string      `xml:"id"`
	Generator string      `xml:"generator"`
	Entries   []AtomEntry `xml:"entry"`
}

type AtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type AtomEntry struct {
	Title     string     `xml:"title"`
	Link      []AtomLink `xml:"link"`
	ID        string     `xml:"id"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Summary   string     `xml:"summary"`
	Category  *AtomCategory `xml:"category,omitempty"`
}

type AtomCategory struct {
	Term string `xml:"term,attr"`
}

// JSONFeed generates JSON Feed 1.1
type JSONFeed struct {
	Version     string         `json:"version"`
	Title       string         `json:"title"`
	HomePageURL string         `json:"home_page_url"`
	FeedURL     string         `json:"feed_url"`
	Description string         `json:"description,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Favicon     string         `json:"favicon,omitempty"`
	Language    string         `json:"language,omitempty"`
	Items       []JSONFeedItem `json:"items"`
}

type JSONFeedItem struct {
	ID            string   `json:"id"`
	URL           string   `json:"url,omitempty"`
	Title         string   `json:"title"`
	ContentHTML   string   `json:"content_html,omitempty"`
	ContentText   string   `json:"content_text,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	DatePublished string   `json:"date_published"`
	DateModified  string   `json:"date_modified,omitempty"`
	Tags          []string `json:"tags,omitempty"`
}

// FeedGenerator generates various feed formats
type FeedGenerator struct {
	title   string
	baseURL string
}

// NewFeedGenerator creates a new feed generator
func NewFeedGenerator(title, baseURL string) *FeedGenerator {
	return &FeedGenerator{
		title:   title,
		baseURL: baseURL,
	}
}

// GenerateRSS generates RSS 2.0 feed from incidents
func (fg *FeedGenerator) GenerateRSS(incidents []storage.Incident) ([]byte, error) {
	items := make([]RSSItem, 0, len(incidents))

	for _, inc := range incidents {
		item := RSSItem{
			Title:       fmt.Sprintf("[%s] %s", inc.Severity, inc.Title),
			Link:        fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID),
			Description: fg.formatIncidentDescription(inc),
			PubDate:     inc.CreatedAt.Format(time.RFC1123Z),
			GUID:        inc.ID,
			Category:    inc.Severity,
		}
		items = append(items, item)
	}

	feed := RSSFeed{
		Version: "2.0",
		Channel: RSSChannel{
			Title:         fg.title + " - Status Updates",
			Link:          fg.baseURL,
			Description:   "Status updates and incident reports",
			Language:      "en-us",
			LastBuildDate: time.Now().Format(time.RFC1123Z),
			Generator:     "Status Monitor",
			Items:         items,
		},
	}

	return xml.MarshalIndent(feed, "", "  ")
}

// GenerateAtom generates Atom 1.0 feed from incidents
func (fg *FeedGenerator) GenerateAtom(incidents []storage.Incident) ([]byte, error) {
	entries := make([]AtomEntry, 0, len(incidents))

	for _, inc := range incidents {
		entry := AtomEntry{
			Title: fmt.Sprintf("[%s] %s", inc.Severity, inc.Title),
			Link: []AtomLink{
				{Href: fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID), Rel: "alternate"},
			},
			ID:        fmt.Sprintf("urn:uuid:%s", inc.ID),
			Updated:   inc.UpdatedAt.Format(time.RFC3339),
			Published: inc.CreatedAt.Format(time.RFC3339),
			Summary:   fg.formatIncidentDescription(inc),
			Category:  &AtomCategory{Term: inc.Severity},
		}
		entries = append(entries, entry)
	}

	feed := AtomFeed{
		Xmlns:     "http://www.w3.org/2005/Atom",
		Title:     fg.title + " - Status Updates",
		Subtitle:  "Status updates and incident reports",
		Link: []AtomLink{
			{Href: fg.baseURL, Rel: "alternate"},
			{Href: fg.baseURL + "/feed/atom", Rel: "self", Type: "application/atom+xml"},
		},
		Updated:   time.Now().Format(time.RFC3339),
		ID:        fg.baseURL,
		Generator: "Status Monitor",
		Entries:   entries,
	}

	output, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}

	return append([]byte(xml.Header), output...), nil
}

// GenerateJSON generates JSON Feed 1.1 from incidents
func (fg *FeedGenerator) GenerateJSON(incidents []storage.Incident) ([]byte, error) {
	items := make([]JSONFeedItem, 0, len(incidents))

	for _, inc := range incidents {
		item := JSONFeedItem{
			ID:            inc.ID,
			URL:           fmt.Sprintf("%s/incidents/%s", fg.baseURL, inc.ID),
			Title:         inc.Title,
			ContentHTML:   fg.formatIncidentHTML(inc),
			ContentText:   fg.formatIncidentDescription(inc),
			Summary:       inc.Message,
			DatePublished: inc.CreatedAt.Format(time.RFC3339),
			DateModified:  inc.UpdatedAt.Format(time.RFC3339),
			Tags:          append([]string{inc.Severity, inc.Status}, inc.AffectedServices...),
		}
		items = append(items, item)
	}

	feed := JSONFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       fg.title + " - Status Updates",
		HomePageURL: fg.baseURL,
		FeedURL:     fg.baseURL + "/feed/json",
		Description: "Status updates and incident reports",
		Language:    "en",
		Items:       items,
	}

	return json.MarshalIndent(feed, "", "  ")
}

func (fg *FeedGenerator) formatIncidentDescription(inc storage.Incident) string {
	desc := fmt.Sprintf("Status: %s\n", inc.Status)
	desc += fmt.Sprintf("Severity: %s\n", inc.Severity)
	if len(inc.AffectedServices) > 0 {
		desc += fmt.Sprintf("Affected: %v\n", inc.AffectedServices)
	}
	desc += fmt.Sprintf("\n%s", inc.Message)

	if len(inc.Updates) > 0 {
		desc += "\n\nUpdates:\n"
		for _, u := range inc.Updates {
			desc += fmt.Sprintf("- [%s] %s: %s\n", u.CreatedAt.Format("Jan 02 15:04"), u.Status, u.Message)
		}
	}

	return desc
}

func (fg *FeedGenerator) formatIncidentHTML(inc storage.Incident) string {
	html := fmt.Sprintf("<p><strong>Status:</strong> %s</p>", inc.Status)
	html += fmt.Sprintf("<p><strong>Severity:</strong> %s</p>", inc.Severity)
	if len(inc.AffectedServices) > 0 {
		html += "<p><strong>Affected Services:</strong></p><ul>"
		for _, svc := range inc.AffectedServices {
			html += fmt.Sprintf("<li>%s</li>", svc)
		}
		html += "</ul>"
	}
	html += fmt.Sprintf("<p>%s</p>", inc.Message)

	if len(inc.Updates) > 0 {
		html += "<h4>Updates</h4><ul>"
		for _, u := range inc.Updates {
			html += fmt.Sprintf("<li><strong>%s</strong> [%s]: %s</li>",
				u.CreatedAt.Format("Jan 02, 2006 15:04 MST"), u.Status, u.Message)
		}
		html += "</ul>"
	}

	return html
}
