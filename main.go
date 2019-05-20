package main

import (
	"encoding/json"
	"fmt"
	"github.com/jpillora/backoff"
	"github.com/mdlayher/untappd"
	"github.com/nickvanw/ircx"
	"github.com/sorcix/irc"
	"io/ioutil"
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

type Config struct {
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Users        []User
	BotName      string `json:"bot_name"`
	Server       string
	Channel      string
	TimeZone     string `json:"time_zone"`
	Location     *time.Location
}

type User struct {
	Name string
}

var config Config
var once sync.Once

// The untappd api limits how many checkins you can query on other users.
// Limit is 300 at the moment.
const CheckinApiLimit int = 300

func readConfigFile(fileName string) (Config, error) {
	body, err := ioutil.ReadFile(fileName)

	var root Config
	err = json.Unmarshal(body, &root)
	if err != nil {
		return root, err
	}

	root.Location, err = time.LoadLocation(root.TimeZone)
	if err != nil {
		return root, err
	}

	return root, nil
}

func isCheckinNew(checkin *untappd.Checkin, checkins []*untappd.Checkin) bool {
	for _, c := range checkins {
		if c.ID == checkin.ID {
			return false
		}
	}

	return true
}

func formatCheckin(checkin *untappd.Checkin) (string, string, string, string) {
	generalInfo := fmt.Sprintf("untappd alert for %s: %s (%s).",
		checkin.User.UserName,
		checkin.Beer.Name,
		checkin.Brewery.Name)
	styleInfo := fmt.Sprintf("  Style: %s   ABV: %0.1f%%",
		checkin.Beer.Style, checkin.Beer.ABV)
	ratingInfo := fmt.Sprintf("  Rating: %0.1f   %s",
		checkin.UserRating,
		checkin.Comment)
	venueInfo := ""
	if checkin.Venue != nil {
		venueInfo = fmt.Sprintf("  Venue: %s", checkin.Venue.Name)
	}

	return generalInfo, styleInfo, ratingInfo, venueInfo
}

func main() {
	var err error
	config, err = readConfigFile("./config.json")
	if err != nil {
		log.Fatal(err)
	}

	bot := ircx.WithTLS(config.Server, config.BotName, nil)
	bot.Config.MaxRetries = 10
	bot.SetLogger(bot.Logger())
	if err := bot.Connect(); err != nil {
		log.Fatal("Unable to dial IRC Server ", err)
	}

	RegisterHandlers(bot)
	bot.HandleLoop()
	log.Println("Exiting..")
}

func RegisterHandlers(bot *ircx.Bot) {
	bot.HandleFunc(irc.RPL_WELCOME, RegisterConnect)
	bot.HandleFunc(irc.PING, PingHandler)
	bot.HandleFunc(irc.RPL_NAMREPLY, JoinedHandler)
}

func RegisterConnect(s ircx.Sender, m *irc.Message) {
	s.Send(&irc.Message{
		Command: irc.JOIN,
		Params:  []string{config.Channel},
	})
}

func PingHandler(s ircx.Sender, m *irc.Message) {
	s.Send(&irc.Message{
		Command:  irc.PONG,
		Params:   m.Params,
		Trailing: m.Trailing,
	})
}

func JoinedHandler(s ircx.Sender, m *irc.Message) {
	log.Printf("Joined channel %s.", config.Channel)
	untappdFunc := func() {
		log.Printf("Starting untappd event loop.")
		go untappdLoop(s)
	}

	once.Do(untappdFunc)
}

func pushMessage(s ircx.Sender, cs chan string, channelName string) {
	// Avoid message flooding the irc server by waiting
	// two seconds between messages
	throttle := time.Tick(2 * time.Second)
	for {
		select {
		case message := <-cs:
			<-throttle
			s.Send(&irc.Message{
				Command:  irc.PRIVMSG,
				Params:   []string{channelName},
				Trailing: message,
			})
		}
	}
}

func getStats(checkins []*untappd.Checkin, beer *untappd.Beer) (float64, float64, float64, int32, *untappd.Checkin) {
	var min float64 = math.MaxFloat64
	var max float64 = -math.MaxFloat64
	var total float64 = 0.0
	var count int32 = 0
	var lastCheckin *untappd.Checkin = nil
	for _, oldCheckin := range checkins {
		if oldCheckin.Beer.ID == beer.ID {
			if lastCheckin == nil || oldCheckin.ID > lastCheckin.ID {
				lastCheckin = oldCheckin
			}
			if oldCheckin.UserRating < min {
				min = oldCheckin.UserRating
			}
			if oldCheckin.UserRating > max {
				max = oldCheckin.UserRating
			}
			total = total + oldCheckin.UserRating
			count = count + 1
		}
	}

	return min, max, total / float64(count), count, lastCheckin
}

func sendCheckinToIrc(checkin *untappd.Checkin, cs chan string, userCheckins map[string][]*untappd.Checkin) {
	// Format the message and add it to the message channel
	general, style, rating, venue := formatCheckin(checkin)
	cs <- general
	cs <- style
	cs <- rating
	if venue != "" {
		cs <- venue
	}

	// Print ratings from the other users
	for user, checkins := range userCheckins {
		if user != checkin.User.UserName {
			min, max, avg, count, lastCheckin := getStats(checkins, checkin.Beer)
			if lastCheckin != nil {
				localTime := time.Time.In(lastCheckin.Created, config.Location)
				created := time.Time.Format(localTime, "02 Jan 2006 15:04")
				stats := ""
				if count > 1 {
					stats = fmt.Sprintf("[%0.1f-%0.1f] %0.1f #%d",
						min, max, avg, count)
				}
				cs <- fmt.Sprintf("    %s rated this on %s: %0.1f  %s  %s", user, created,
					lastCheckin.UserRating, lastCheckin.Comment, stats)
			}
		}
	}
}

func logCheckin(checkin *untappd.Checkin) {
	general, style, rating, venue := formatCheckin(checkin)
	log.Printf("%s  %s  %s  %s", general, style, rating, venue)
}

func calculatePollInterval(numUsers int) int {
	// Untappd allows (only!) 100 api calls per hour
	numApiCalls := 100
	// Evenly distribute these calls for the different users
	numCallsPerUser := float64(numApiCalls) / float64(numUsers)
	// And round up to make sure we stay within the rate limit
	return int(math.Ceil(60.0 / numCallsPerUser))
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// Get all checkins for a given user.
func getAllCheckins(userName string, client *untappd.Client) []*untappd.Checkin {
	log.Printf("Getting checkins for %s", userName)

	nCheckins := 50
	maxId := math.MaxInt32
	allCheckins := make([]*untappd.Checkin, 0)

	b := &backoff.Backoff{
		Min:    60 * time.Second,
		Max:    30 * time.Minute,
		Factor: 2,
		Jitter: true,
	}

	for {
		if len(allCheckins) >= CheckinApiLimit {
			log.Printf("Api limit reached for %s.", userName)
			return allCheckins
		}

		// The untappd api only allows you to get the lastest 300 checkins
		// for other users (for non-obvious reasons).
		limit := min(CheckinApiLimit-len(allCheckins), nCheckins)
		log.Printf("Getting %d checkins %d through %d. Number of checkins: %d", limit, 0, maxId, len(allCheckins))
		checkins, _, err := client.User.CheckinsMinMaxIDLimit(userName, 0, maxId, limit)
		if err != nil {
			d := b.Duration()
			log.Printf("%s, retrying in %s", err, d)
			time.Sleep(d)
			continue
		}

		//connected
		b.Reset()

		log.Printf("Got %d checkins (%s, %d)", len(checkins), userName, maxId)
		if len(checkins) == 0 {
			return allCheckins
		}

		allCheckins = append(allCheckins, checkins...)
		maxId = checkins[len(checkins)-1].ID
	}

	return allCheckins
}

func getCheckins(userName string, client *untappd.Client) []*untappd.Checkin {
	b := &backoff.Backoff{
		Min:    60 * time.Second,
		Max:    30 * time.Minute,
		Factor: 2,
		Jitter: true,
	}

	for {
		checkins, _, err := client.User.Checkins(userName)
		if err != nil {
			d := b.Duration()
			log.Printf("%s, retrying in %s", err, d)
			time.Sleep(d)
			continue
		} else {
			return checkins
		}
	}

	return nil
}

func getUserStats(checkins []*untappd.Checkin) (int, float64, float64) {
	var mean, stdev float64
	var count int = len(checkins)

	sum := 0.0
	for _, checkin := range checkins {
		sum = sum + checkin.UserRating
	}
	mean = sum / float64(count)

	for _, checkin := range checkins {
		stdev += math.Pow(checkin.UserRating - mean, 2)
	}

	stdev = math.Sqrt(stdev / float64(count))
	return count, mean, stdev
}

// byCheckinTime implements sort.Interface for []*untappd.Checkin.
type byCheckinTime []*untappd.Checkin

func (b byCheckinTime) Len() int               { return len(b) }
func (b byCheckinTime) Less(i int, j int) bool { return b[i].Created.Before(b[j].Created) }
func (b byCheckinTime) Swap(i int, j int)      { b[i], b[j] = b[j], b[i] }

func untappdLoop(s ircx.Sender) {
	client, err := untappd.NewClient(
		config.ClientId,
		config.ClientSecret,
		nil,
	)

	if err != nil {
		log.Fatal(err)
	}

	pollInterval := calculatePollInterval(len(config.Users))
	log.Printf("Polling interval: %d min", pollInterval)

	// Channel for messages to be pushed to irc
	ircMessages := make(chan string, 30)
	go pushMessage(s, ircMessages, config.Channel)

	// Generate map of checkins for each user
	userCheckins := make(map[string][]*untappd.Checkin)
	for _, user := range config.Users {
		userCheckins[user.Name] = getAllCheckins(user.Name, client)
	}

	// Generate some statistics for all users
	message := fmt.Sprintf("Statistics for up to %d checkins (untappd api limit).",
		CheckinApiLimit)
	ircMessages <- message
	for user, checkins := range userCheckins {

		count, avg, stdev := getUserStats(checkins)
		message := fmt.Sprintf("untappd stats for %s: %d checkins with %0.2f average rating [stdev: %0.2f)].",
			user, count, avg, stdev)
		ircMessages <- message
		log.Println(message)
	}

	for {
		log.Printf("Checking %d users.\n", len(config.Users))
		for _, user := range config.Users {
			checkins := getCheckins(user.Name, client)

			// Sort to get oldest checkin first
			sort.Sort(byCheckinTime(checkins))
			for _, c := range checkins {
				// Print all new checkins since last poll
				if isCheckinNew(c, userCheckins[user.Name]) {
					userCheckins[user.Name] = append(userCheckins[user.Name], c)
					sendCheckinToIrc(c, ircMessages, userCheckins)
					logCheckin(c)
				}
			}
			sort.Sort(byCheckinTime(userCheckins[user.Name]))
		}
		time.Sleep(time.Duration(pollInterval) * time.Minute)
	}
}
