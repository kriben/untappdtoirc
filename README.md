# untappdtoirc 

Irc bot which reads checkins from untappd and push them to irc channel.

## Configuration

Example config:

```
{
    "client_id" : "untappd id",
    "client_secret" : "untappd client secret",
    "users" : [
        { "name": "peter"},
        { "name": "paul"},
        { "name": "mary"}
    ],
    "bot_name": "untappdbot",
    "channel": "#channel",
    "server": "chat.freenode.org:6667"
}
```

## Usage

```
$ ./untappdtoirc
2015/04/13 20:34:12 Connected to chat.freenode.org:6667
2015/04/13 20:34:26 Joined channel #channel.
2015/04/13 20:34:26 Checking 2 users.
```

## Misc

Licensed under the FreeBSD License (aka the "Simplified BSD License"). See the LICENSE file for details.

Author: Kristian Bendiksen <kristian.bendiksen@gmail.com>
