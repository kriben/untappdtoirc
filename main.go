package main

import (
	"encoding/json"
	"fmt"
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

func isCheckinNew(checkin *untappd.Checkin, lastCheckinTimes map[string]time.Time) bool {
	lastCheckinTime, ok := lastCheckinTimes[checkin.User.UserName]

	// User will not be in the map on first iteration: treat these
	// as old to avoid repeating old checkins when program starts
	if ok == false {
		return false
	}

	return checkin.Created.After(lastCheckinTime)
}

func formatCheckin(checkin *untappd.Checkin) (string, string, string) {
	generalInfo := fmt.Sprintf("untappd alert for %s: %s (%s).",
		checkin.User.UserName,
		checkin.Beer.Name,
		checkin.Brewery.Name)
	styleInfo := fmt.Sprintf("  Style: %s   ABV: %0.1f%%",
		checkin.Beer.Style, checkin.Beer.ABV)
	ratingInfo := fmt.Sprintf("  Rating: %0.1f   %s\n",
		checkin.UserRating,
		checkin.Comment)
	return generalInfo, styleInfo, ratingInfo
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
		default:
		}
	}
}

func sendCheckinToIrc(checkin *untappd.Checkin, cs chan string) {
	// Format the message and add it to the message channel
	general, style, rating := formatCheckin(checkin)
	cs <- general
	cs <- style
	cs <- rating
}

func logCheckin(checkin *untappd.Checkin) {
	general, style, rating := formatCheckin(checkin)
	log.Printf("%s  %s  %s", general, style, rating)
}

func calculatePollInterval(numUsers int) int {
	// Untappd allows (only!) 100 api calls per hour
	numApiCalls := 100
	// Evenly distribute these calls for the different users
	numCallsPerUser := float64(numApiCalls) / float64(numUsers)
	// And round up to make sure we stay within the rate limit
	return int(math.Ceil(60.0 / numCallsPerUser))
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

	lastCheckinTimes := make(map[string]time.Time)
	for {
		log.Printf("Checking %d users.\n", len(config.Users))
		for _, user := range config.Users {
			checkins, _, err := client.User.Checkins(user.Name)
			if err != nil {
				log.Fatal(err)
			}

			// Sort to get oldest checkin first
			sort.Sort(byCheckinTime(checkins))
			for _, c := range checkins {
				// Print all new checkins since last poll
				if isCheckinNew(c, lastCheckinTimes) {
					sendCheckinToIrc(c, ircMessages)
					logCheckin(c)
				}
			}

			// Keep track of the last checkin we have printed
			if len(checkins) > 0 {
				lastCheckin := checkins[len(checkins)-1]
				lastCheckinTimes[lastCheckin.User.UserName] = lastCheckin.Created
			}

		}
		time.Sleep(time.Duration(pollInterval) * time.Minute)
	}
}
