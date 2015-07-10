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
	"time"
)

type Config struct {
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Users        []User
	BotName      string `json:"bot_name"`
	Server       string
	Channel      string
}

type User struct {
	Name string
}

var config Config

func readConfigFile(fileName string) (Config, error) {
	body, err := ioutil.ReadFile(fileName)

	var root Config
	err = json.Unmarshal(body, &root)
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

	bot := ircx.Classic(config.Server, config.BotName)
	if err := bot.Connect(); err != nil {
		log.Panicln("Unable to dial IRC Server ", err)
	}

	RegisterHandlers(bot)
	bot.CallbackLoop()
	log.Println("Exiting..")
}

func RegisterHandlers(bot *ircx.Bot) {
	bot.AddCallback(irc.RPL_WELCOME, ircx.Callback{Handler: ircx.HandlerFunc(RegisterConnect)})
	bot.AddCallback(irc.PING, ircx.Callback{Handler: ircx.HandlerFunc(PingHandler)})
	bot.AddCallback(irc.RPL_NAMREPLY, ircx.Callback{Handler: ircx.HandlerFunc(JoinedHandler)})
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
	go untappdLoop(s)
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
			for i := len(checkins) - 1; i >= 0; i-- {
				oldCheckin := checkins[i]
				if oldCheckin.Beer.ID == checkin.Beer.ID {
					created := time.Time.Format(oldCheckin.Created, "02 Jan 2006 15:04")
					cs <- fmt.Sprintf("    %s rated this on %s: %0.1f  %s", user, created,
						oldCheckin.UserRating, oldCheckin.Comment)
					break
				}
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

// Get all checkins for a given user.
func getAllCheckins(userName string, client *untappd.Client) []*untappd.Checkin {
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
		checkins, _, err := client.User.CheckinsMinMaxIDLimit(userName, 0, maxId, nCheckins)
		if err != nil {
			d := b.Duration()
			log.Printf("%s, retrying in %s", err, d)
			time.Sleep(d)
			continue
		}

		//connected
		b.Reset()

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
	for user, checkins := range userCheckins {
		totalRating := 0.0
		for _, checkin := range checkins {
			totalRating = totalRating + checkin.UserRating
		}
		message := fmt.Sprintf("untappd stats for %s: %d checkins with %0.2f average rating.",
			user, len(checkins), totalRating/float64(len(checkins)))
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
		}
		time.Sleep(time.Duration(pollInterval) * time.Minute)
	}
}
