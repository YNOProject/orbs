/*
	Copyright (C) 2021-2022  The YNOproject Developers

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package server

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

var (
	clients sync.Map
	session = &Session{}
)

type Session struct {
	lastId int
}

func initSession() {
	// we need a sender
	sender := SessionClient{}

	scheduler.Every(5).Seconds().Do(func() {
		sender.broadcast("pc", getSessionClientsLen())
		sendPartyUpdate()
	})
}

func handleSession(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, http.Header{"Sec-Websocket-Protocol": {r.Header.Get("Sec-Websocket-Protocol")}})
	if err != nil {
		log.Println(err)
		return
	}

	var playerToken string
	if token, ok := r.URL.Query()["token"]; ok && len(token[0]) == 32 {
		playerToken = token[0]
	}

	joinSessionWs(conn, getIp(r), playerToken)
}

func joinSessionWs(conn *websocket.Conn, ip string, token string) {
	client := &SessionClient{
		conn:      conn,
		ip:        ip,
		writerEnd: make(chan bool, 1),
		send:      make(chan []byte, 16),
		receive:   make(chan []byte, 16),
	}

	var banned bool
	if token != "" {
		client.uuid, client.name, client.rank, client.badge, banned, client.muted = getPlayerDataFromToken(token)
	}

	if client.uuid != "" {
		client.account = true
	} else {
		client.uuid, banned, client.muted = getOrCreatePlayerData(ip)
	}

	if banned {
		writeErrLog(client.uuid, "sess", "player is banned")
		return
	}

	if client, ok := clients.Load(client.uuid); ok {
		client.(*SessionClient).disconnect()
	}

	var sameIp int
	clients.Range(func(_, v any) bool {
		if v.(*SessionClient).ip == ip {
			sameIp++
		}

		return true
	})
	if sameIp > 3 {
		writeErrLog(client.uuid, "sess", "too many connections from ip")
		return
	}

	if client.badge == "" {
		client.badge = "null"
	}

	client.id = session.lastId
	session.lastId++

	client.spriteName, client.spriteIndex, client.systemName = getPlayerGameData(client.uuid)

	go client.msgWriter()

	// register client to the clients list
	clients.Store(client.uuid, client)

	go client.msgProcessor()
	go client.msgReader()

	writeLog(client.uuid, "sess", "connect", 200)
}

func (sender *SessionClient) broadcast(segments ...any) {
	clients.Range(func(_, v any) bool {
		v.(*SessionClient).sendMsg(segments...)

		return true
	})
}

func (sender *SessionClient) processMsg(msg []byte) (err error) {
	if !utf8.Valid(msg) {
		return errInvalidUTF8
	}

	switch msgFields := strings.Split(string(msg), delim); msgFields[0] {
	case "i": // player info
		err = sender.handleI()
	case "name": // nick set
		err = sender.handleName(msgFields)
	case "ploc": // previous location
		err = sender.handlePloc(msgFields)
	case "gsay": // global say
		err = sender.handleGSay(msgFields)
	case "psay": // party say
		err = sender.handlePSay(msgFields)
	case "pt": // party update
		if sender.handlePt() != nil {
			sender.sendMsg("pt", "null")
		}
	case "ep": // event period
		err = sender.handleEp()
	case "e": // event list
		err = sender.handleE()
	default:
		err = errUnkMsgType
	}
	if err != nil {
		return err
	}

	writeLog(sender.uuid, "sess", string(msg), 200)

	return nil
}

func getSessionClientsLen() (length int) {
	clients.Range(func(_, _ any) bool {
		length++

		return true
	})

	return length
}
