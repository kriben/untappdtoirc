package main

import (
	"encoding/json"
	"fmt"
	"github.com/kriben/untappd"
	"github.com/nickvanw/ircx"
	"github.com/sorcix/irc"
	"io/ioutil"
	"log"
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

func formatCheckin(checkin *untappd.Checkin) string {
	return fmt.Sprintf("untappd alert: %s had %s (%s). Style: %s Rating: %0.1f \"%s\"\n",
		checkin.User.UserName,
		checkin.Beer.Name,
		checkin.Brewery.Name,
		checkin.Beer.Style,
		checkin.UserRating,
		checkin.Comment,
	)
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

func untappdLoop(s ircx.Sender) {
	client, err := untappd.NewClient(
		config.ClientId,
		config.ClientSecret,
		nil,
	)

	if err != nil {
		log.Fatal(err)
	}

	lastCheckinTimes := make(map[string]time.Time)
	for {
		log.Printf("Checking %d users.\n", len(config.Users))
		for _, user := range config.Users {
			checkins, _, err := client.User.Checkins(user.Name)
			if err != nil {
				log.Fatal(err)
			}

			for _, c := range checkins {
				if isCheckinNew(c, lastCheckinTimes) {
					message := formatCheckin(c)
					s.Send(&irc.Message{
						Command:  irc.PRIVMSG,
						Params:   []string{config.Channel},
						Trailing: message,
					})
				}
			}

			if len(checkins) > 0 {
				lastCheckinTimes[checkins[0].User.UserName] = checkins[0].Created
			}
		}
		time.Sleep(5 * time.Minute)
	}
}
