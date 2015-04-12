package main

import (
	"encoding/json"
	"fmt"
	"github.com/kriben/untappd"
	"io/ioutil"
	"log"
	"time"
)

type Config struct {
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Users        []User
}

type User struct {
	Name string
}

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

func printCheckin(checkin *untappd.Checkin) {
	fmt.Printf("%s  %s  %s  %s  %0.1f %s %0.1f %+v\n",
		checkin.User.UserName,
		checkin.Beer.Name,
		checkin.Brewery.Name,
		checkin.Beer.Style,
		checkin.Beer.ABV,
		checkin.Comment,
		checkin.UserRating,
		checkin.Created,
	)
}

func main() {
	config, err := readConfigFile("./config.json")
	if err != nil {
		log.Fatal(err)
	}

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
		fmt.Printf("Checking %d users.\n", len(config.Users))
		for _, user := range config.Users {
			checkins, _, err := client.User.Checkins(user.Name)
			if err != nil {
				log.Fatal(err)
			}

			for _, c := range checkins {
				if isCheckinNew(c, lastCheckinTimes) {
					printCheckin(c)
				}
			}

			if len(checkins) > 0 {
				lastCheckinTimes[checkins[0].User.UserName] = checkins[0].Created
			}
		}
		fmt.Printf("%+v\n", lastCheckinTimes)
		time.Sleep(1 * time.Minute)
	}
}
