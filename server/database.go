/*
	Copyright (C) 2021-2024  The YNOproject Developers

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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var db *sql.DB

func getDatabaseConn(user, password, addr, database string) *sql.DB {
	conn, err := sql.Open("mysql", fmt.Sprintf("%s:%s@%s/%s?parseTime=true", user, password, addr, database))
	if err != nil {
		panic(err)
	}

	return conn
}

func getOrCreatePlayerData(ip string) (uuid string, banned bool, muted bool) {
	err := db.QueryRow("SELECT uuid, banned, muted FROM players WHERE ip = ?", ip).Scan(&uuid, &banned, &muted)
	if err != nil {
		if err != sql.ErrNoRows {
			return "", false, false
		}

		// create new guest account
		uuid = randString(16)
		createPlayerData(ip, uuid, banned)

		// recheck moderation status
		if !banned {
			banned, muted = getPlayerModerationStatus(uuid)
		}
	}

	return uuid, banned, muted
}

func getPlayerDataFromToken(token string) (uuid string, name string, rank int, badge string, banned bool, muted bool) {
	err := db.QueryRow("SELECT a.uuid, a.user, pd.rank, a.badge, pd.banned, pd.muted FROM accounts a JOIN playerSessions ps ON ps.uuid = a.uuid JOIN players pd ON pd.uuid = a.uuid WHERE ps.sessionId = ? AND NOW() < ps.expiration", token).Scan(&uuid, &name, &rank, &badge, &banned, &muted)
	if err != nil {
		return "", "", 0, "", false, false
	}

	return uuid, name, rank, badge, banned, muted
}

func getPlayerRank(uuid string) (rank int) {
	if client, ok := clients.Load(uuid); ok {
		return client.rank // return rank from session if client is connected
	}

	err := db.QueryRow("SELECT rank FROM players WHERE uuid = ?", uuid).Scan(&rank)
	if err != nil {
		return 0
	}

	return rank
}

func tryBanPlayer(senderUuid string, recipientUuid string) error { // called by api only
	if getPlayerRank(senderUuid) <= getPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-ban")
	}
	return banPlayerUnchecked(recipientUuid, true)
}

func banPlayerUnchecked(recipientUuid string, updateDb bool) error {
	if updateDb {
		_, err := db.Exec("UPDATE players SET banned = 1 WHERE uuid = ?", recipientUuid)
		if err != nil {
			return err
		}
	}

	if client, ok := clients.Load(recipientUuid); ok {
		name := client.name
		if client.roomC != nil {
			client.roomC.cancel()
		}

		client.cancel()

		if name != "" {
			msg := fmt.Sprintf("*%s has been banned.*", name)
			var sender *SessionClient
			sender.broadcast(buildMsg("p", "0000000000000000", "YNO", "", 2, true, "null", [5]int{}))
			sender.broadcast(buildMsg("gsay", "0000000000000000", "0000", "0000", "0", 0, 0, msg, randString(12)))
		}
	}

	return nil
}

func tryUnbanPlayer(senderUuid string, recipientUuid string) error { // called by api only
	if getPlayerRank(senderUuid) <= getPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-unban")
	}

	_, err := db.Exec("UPDATE players SET banned = 0 WHERE uuid = ?", recipientUuid)
	if err != nil {
		return err
	}

	return nil
}

func tryMutePlayer(senderUuid string, recipientUuid string) error { // called by api only
	if getPlayerRank(senderUuid) <= getPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-mute")
	}
	return mutePlayerUnchecked(recipientUuid, true)
}

func mutePlayerUnchecked(recipientUuid string, updateDb bool) error {
	if updateDb {
		_, err := db.Exec("UPDATE players SET muted = 1 WHERE uuid = ?", recipientUuid)
		if err != nil {
			return err
		}
	}

	if client, ok := clients.Load(recipientUuid); ok { // mute client if they're connected
		client.muted = true

		if name := client.name; name != "" {
			msg := fmt.Sprintf("*%s has been muted.*", name)
			var sender *SessionClient
			sender.broadcast(buildMsg("p", "0000000000000000", "YNO", "", 2, true, "null", [5]int{}))
			sender.broadcast(buildMsg("gsay", "0000000000000000", "0000", "0000", "0", 0, 0, msg, randString(12)))
		}
	}

	return nil
}

func tryUnmutePlayer(senderUuid string, recipientUuid string) error { // called by api only
	if getPlayerRank(senderUuid) <= getPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-unmute")
	}

	_, err := db.Exec("UPDATE players SET muted = 0 WHERE uuid = ?", recipientUuid)
	if err != nil {
		return err
	}

	if client, ok := clients.Load(recipientUuid); ok { // unmute client if they're connected
		client.muted = false
	}

	return nil
}

func tryChangePlayerUsername(senderUuid string, recipientUuid string, newUsername string) error { // called by api only
	if getPlayerRank(senderUuid) <= getPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	existingUuid, err := getUuidFromName(newUsername)
	if err != nil {
		return err
	}
	if existingUuid != "" {
		return errors.New("user with new username already exists")
	}

	_, err = db.Exec("UPDATE accounts SET user = ? WHERE uuid = ?", newUsername, recipientUuid)
	if err != nil {
		return err
	}

	if client, ok := clients.Load(recipientUuid); ok { // change client username if they're connected
		client.name = newUsername

		if client.roomC != nil {
			client.roomC.broadcast(buildMsg("name", client.id, newUsername)) // broadcast name change to room if client is in one
		}
	}

	return nil
}

func handleTempBan(uuid string, targetUuid string) (duration int, err error) {
	var userExists int
	db.QueryRow("SELECT EXISTS (SELECT * FROM accounts WHERE uuid = ?)", uuid).Scan(&userExists)

	if userExists == 0 {
		return "", errors.New("user not found")
	}

	err = tryBanPlayer(uuid, targetUuid)
	
	if err != nil {
		handleInternalError(http.ResponseWriter, *http.Request, err)
		return
	}
	
	_, err := db.Exec("INSERT INTO playerModerationAction (uuid, action, expiry) VALUES (?, 'tempban', ?)", uuid, duration)
	if err != nil {
		return err
	}
	
	tempBan := time.NewTimer(duration * 60 * time.Second)
	
	<-tempBan.C
	err = tryUnbanPlayer(uuid, targetUuid)
	if err != nil {
		handleInternalError(http.ResponseWriter, *http.Request, err)
		return
	}
	
	w.Write([]byte("ok"))
}

func getPlayerMedals(uuid string) (medals [5]int) {
	if client, ok := clients.Load(uuid); ok {
		return client.medals // return medals from session if client is connected
	}

	err := db.QueryRow("SELECT pgd.medalCountBronze, pgd.medalCountSilver, pgd.medalCountGold, pgd.medalCountPlatinum, pgd.medalCountDiamond FROM players pd LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid WHERE pd.uuid = ? AND pgd.game = ?", uuid, config.gameName).Scan(&medals[0], &medals[1], &medals[2], &medals[3], &medals[4])
	if err != nil {
		return [5]int{}
	}

	return medals
}

func getPlayerModerationStatus(uuid string) (banned bool, muted bool) {
	err := db.QueryRow("SELECT banned, muted FROM players WHERE uuid = ?", uuid).Scan(&banned, &muted)
	if err != nil {
		return false, false
	}

	return banned, muted
}

func tryBlockPlayer(uuid string, targetUuid string) error { // called by api only
	if getPlayerRank(uuid) < getPlayerRank(targetUuid) {
		return errors.New("insufficient rank")
	}

	if uuid == targetUuid {
		return errors.New("attempted self-block")
	}

	_, err := db.Exec("INSERT IGNORE INTO playerBlocks (uuid, targetUuid, timestamp) VALUES (?, ?, UTC_TIMESTAMP())", uuid, targetUuid)
	if err != nil {
		return err
	}

	return nil
}

func tryUnblockPlayer(uuid string, targetUuid string) error { // called by api only
	_, err := db.Exec("DELETE FROM playerBlocks WHERE uuid = ? AND targetUuid = ?", uuid, targetUuid)
	if err != nil {
		return err
	}

	return nil
}

func getBlockedPlayerData(uuid string) ([]*PlayerListData, error) {
	var blockedPlayers []*PlayerListData

	results, err := db.Query("SELECT pd.uuid, COALESCE(a.user, pgd.name), pd.rank, CASE WHEN a.user IS NULL THEN 0 ELSE 1 END, COALESCE(a.badge, ''), pgd.systemName, pgd.spriteName, pgd.spriteIndex, pgd.medalCountBronze, pgd.medalCountSilver, pgd.medalCountGold, pgd.medalCountPlatinum, pgd.medalCountDiamond FROM players pd JOIN playerBlocks pb ON pb.targetUuid = pd.uuid AND pb.uuid = ? JOIN playerGameData pgd ON pgd.uuid = pd.uuid LEFT JOIN accounts a ON a.uuid = pd.uuid WHERE pgd.game = ? ORDER BY pb.timestamp", uuid, config.gameName)
	if err != nil {
		return blockedPlayers, err
	}

	defer results.Close()

	for results.Next() {
		var blockedPlayer PlayerListData

		err := results.Scan(&blockedPlayer.Uuid, &blockedPlayer.Name, &blockedPlayer.Rank, &blockedPlayer.Account, &blockedPlayer.Badge, &blockedPlayer.SystemName, &blockedPlayer.SpriteName, &blockedPlayer.SpriteIndex, &blockedPlayer.Medals[0], &blockedPlayer.Medals[1], &blockedPlayer.Medals[2], &blockedPlayer.Medals[3], &blockedPlayer.Medals[4])
		if err != nil {
			return blockedPlayers, err
		}

		blockedPlayers = append(blockedPlayers, &blockedPlayer)
	}

	return blockedPlayers, nil
}

func createPlayerData(ip string, uuid string, banned bool) error {
	_, err := db.Exec("INSERT INTO players (ip, uuid, banned) VALUES (?, ?, ?)", ip, uuid, banned)
	if err != nil {
		return err
	}

	return nil
}

func getPlayerGameData(uuid string) (spriteName string, spriteIndex int, systemName string) {
	err := db.QueryRow("SELECT pgd.spriteName, pgd.spriteIndex, pgd.systemName FROM players pd LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid WHERE pd.uuid = ? AND pgd.game = ?", uuid, config.gameName).Scan(&spriteName, &spriteIndex, &systemName)
	if err != nil {
		return "", 0, ""
	}

	return spriteName, spriteIndex, systemName
}

func (c *SessionClient) addOrUpdatePlayerGameData() error {
	_, err := db.Exec("INSERT INTO playerGameData (uuid, game, online) VALUES (?, ?, 1) ON DUPLICATE KEY UPDATE online = 1, timestampLastActive = UTC_TIMESTAMP()", c.uuid, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func (c *SessionClient) updatePlayerGameActivity(online bool) error {
	_, err := db.Exec("UPDATE playerGameData SET name = ?, systemName = ?, spriteName = ?, spriteIndex = ?, online = ?, timestampLastActive = UTC_TIMESTAMP() WHERE uuid = ? AND game = ?", c.name, c.system, c.sprite, c.spriteIndex, online, c.uuid, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func getPlayerInfo(ip string) (uuid string, name string, rank int) {
	err := db.QueryRow("SELECT pd.uuid, pgd.name, pd.rank FROM players pd LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid WHERE pd.ip = ? AND (pgd.uuid IS NULL OR pgd.game = ?)", ip, config.gameName).Scan(&uuid, &name, &rank)
	if err != nil {
		return "", "", 0
	}

	return uuid, name, rank
}

func getPlayerInfoFromToken(token string) (uuid string, name string, rank int, badge string, badgeSlotRows int, badgeSlotCols int, screenshotLimit int) {
	err := db.QueryRow("SELECT a.uuid, a.user, pd.rank, a.badge, a.badgeSlotRows, a.badgeSlotCols, a.screenshotLimit FROM accounts a JOIN playerSessions ps ON ps.uuid = a.uuid JOIN players pd ON pd.uuid = a.uuid WHERE ps.sessionId = ? AND NOW() < ps.expiration", token).Scan(&uuid, &name, &rank, &badge, &badgeSlotRows, &badgeSlotCols, &screenshotLimit)
	if err != nil {
		return "", "", 0, "", 0, 0, 0
	}

	return uuid, name, rank, badge, badgeSlotRows, badgeSlotCols, screenshotLimit
}

func updatePlayerActivity() error {
	_, err := db.Exec("UPDATE accounts SET inactive = CASE WHEN timestampLoggedIn IS NULL OR timestampLoggedIn < DATE_SUB(NOW(), INTERVAL 3 MONTH) THEN 1 ELSE 0 END")
	if err != nil {
		return err
	}

	return nil
}

func writeGlobalChatMessage(msgId, uuid, mapId, prevMapId, prevLocations string, x, y int, contents string) error {
	_, err := db.Exec("INSERT INTO chatMessages (msgId, game, uuid, mapId, prevMapId, prevLocations, x, y, contents) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", msgId, config.gameName, uuid, mapId, prevMapId, prevLocations, x, y, contents)
	if err != nil {
		return err
	}

	return nil
}

func updatePlayerLastChatMessage(uuid, lastMsgId string, party bool) error {
	query := "UPDATE playerGameData SET "

	if party {
		query += "lastPartyMsgId"
	} else {
		query += "lastGlobalMsgId"
	}

	query += " = ? WHERE uuid = ? AND game = ?"

	_, err := db.Exec(query, lastMsgId, uuid, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func getChatMessageHistory(uuid string, globalMsgLimit, partyMsgLimit int, lastMsgId string) (*ChatHistory, error) {
	var chatHistory ChatHistory

	partyId, err := getPlayerPartyId(uuid)
	if err != nil {
		return &chatHistory, err
	}

	var query string

	selectClause := "SELECT cm.msgId, cm.uuid, cm.mapId, cm.prevMapId, cm.prevLocations, cm.x, cm.y, cm.contents, cm.timestamp, "
	globalSelectClause := selectClause + "0"
	partySelectClause := selectClause + "1"

	fromClause := " FROM chatMessages cm JOIN players pd ON pd.uuid = cm.uuid JOIN playerGameData pgd ON pgd.uuid = pd.uuid AND pgd.game = cm.game "

	whereClause := "WHERE cm.game = ? AND pd.banned = 0"

	if lastMsgId != "" {
		whereClause += " AND cm.timestamp > (SELECT cm2.timestamp FROM chatMessages cm2 WHERE cm2.msgId = ?)"
	}

	globalWhereClause := whereClause + " AND cm.partyId IS NULL AND (pgd.lastGlobalMsgId IS NULL OR cm.timestamp > (SELECT cmg.timestamp FROM chatMessages cmg WHERE cmg.msgId = pgd.lastGlobalMsgId)) ORDER BY 9 DESC"
	partyWhereClause := whereClause + " AND cm.partyId = ? AND (pgd.lastPartyMsgId IS NULL OR cm.timestamp > (SELECT cmp.timestamp FROM chatMessages cmp WHERE cmp.msgId = pgd.lastPartyMsgId)) ORDER BY 9 DESC"

	var messageQueryArgs []interface{}

	messageQueryArgs = append(messageQueryArgs, config.gameName)

	if lastMsgId != "" {
		messageQueryArgs = append(messageQueryArgs, lastMsgId)
	}

	messageQueryArgs = append(messageQueryArgs, globalMsgLimit)

	query += "("

	if partyId == 0 {
		query += globalSelectClause + fromClause + globalWhereClause + " LIMIT ?"
	} else {
		messageQueryArgs = append(messageQueryArgs, config.gameName)

		if lastMsgId != "" {
			messageQueryArgs = append(messageQueryArgs, lastMsgId)
		}

		messageQueryArgs = append(messageQueryArgs, partyId, partyMsgLimit)

		query += globalSelectClause + fromClause + globalWhereClause + " LIMIT ?) UNION (" + partySelectClause + fromClause + partyWhereClause + " LIMIT ?"
	}

	query += ") ORDER BY 9"

	messageResults, err := db.Query(query, messageQueryArgs...)
	if err != nil {
		return &chatHistory, err
	}

	defer messageResults.Close()

	for messageResults.Next() {
		var chatMessage ChatMessage

		err := messageResults.Scan(&chatMessage.MsgId, &chatMessage.Uuid, &chatMessage.MapId, &chatMessage.PrevMapId, &chatMessage.PrevLocations, &chatMessage.X, &chatMessage.Y, &chatMessage.Contents, &chatMessage.Timestamp, &chatMessage.Party)
		if err != nil {
			return &chatHistory, err
		}

		chatHistory.Messages = append(chatHistory.Messages, &chatMessage)
	}

	var firstTimestamp time.Time
	var lastTimestamp time.Time

	if len(chatHistory.Messages) != 0 {
		firstTimestamp = chatHistory.Messages[0].Timestamp
		lastTimestamp = chatHistory.Messages[len(chatHistory.Messages)-1].Timestamp
	}

	playersQuery := "SELECT DISTINCT pd.uuid, COALESCE(a.user, pgd.name), pd.rank, CASE WHEN a.user IS NULL THEN 0 ELSE 1 END, COALESCE(a.badge, ''), pgd.systemName, pgd.medalCountBronze, pgd.medalCountSilver, pgd.medalCountGold, pgd.medalCountPlatinum, pgd.medalCountDiamond FROM players pd JOIN playerGameData pgd ON pgd.uuid = pd.uuid LEFT JOIN accounts a ON a.uuid = pd.uuid WHERE pgd.game = ? AND EXISTS (SELECT cm.uuid FROM chatMessages cm WHERE cm.uuid = pd.uuid AND cm.game = pgd.game AND cm.timestamp BETWEEN ? AND ? "

	var playerQueryArgs []interface{}

	playerQueryArgs = append(playerQueryArgs, config.gameName, firstTimestamp, lastTimestamp)

	if partyId == 0 {
		playersQuery += "AND cm.partyId IS NULL"
	} else {
		playersQuery += "AND (cm.partyId IS NULL OR cm.partyId = ?)"

		playerQueryArgs = append(playerQueryArgs, partyId)
	}

	playersQuery += ")"

	playerResults, err := db.Query(playersQuery, playerQueryArgs...)
	if err != nil {
		return &chatHistory, err
	}

	defer playerResults.Close()

	for playerResults.Next() {
		var chatPlayer ChatPlayer

		err := playerResults.Scan(&chatPlayer.Uuid, &chatPlayer.Name, &chatPlayer.Rank, &chatPlayer.Account, &chatPlayer.Badge, &chatPlayer.SystemName, &chatPlayer.Medals[0], &chatPlayer.Medals[1], &chatPlayer.Medals[2], &chatPlayer.Medals[3], &chatPlayer.Medals[4])
		if err != nil {
			return &chatHistory, err
		}

		chatHistory.Players = append(chatHistory.Players, &chatPlayer)
	}

	return &chatHistory, nil
}

func deleteOldChatMessages() error {
	_, err := db.Exec("DELETE FROM chatMessages WHERE timestamp < DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 DAY)")
	if err != nil {
		return err
	}

	return nil
}

func getGameLocationByName(locationName string) (gameLocation *GameLocation, err error) {
	gameLocation = &GameLocation{}
	var mapIdsJson []byte
	err = db.QueryRow("SELECT id, game, title, mapIds FROM gameLocations WHERE title = ? AND game = ?", locationName, config.gameName).Scan(&gameLocation.Id, &gameLocation.Game, &gameLocation.Name, &mapIdsJson)
	if err != nil {
		if err == sql.ErrNoRows {
			var matchingEventLocation *EventLocationData

			if config.gameName == "2kki" {
				matchingEventLocation, err = get2kkiEventLocationData(locationName)
				if err != nil {
					return gameLocation, err
				}
			} else {
				for _, eventLocation := range gameEventLocations[config.gameName] {
					if eventLocation.Title == locationName {
						matchingEventLocation = eventLocation
						break
					}
				}
			}

			if matchingEventLocation != nil {
				mapIdsJson, err = json.Marshal(matchingEventLocation.MapIds)
				if err != nil {
					return gameLocation, err
				}

				res, err := db.Exec("INSERT INTO gameLocations (game, title, titleJP, depth, minDepth, mapIds) VALUES (?, ?, ?, ?, ?, ?)", config.gameName, matchingEventLocation.Title, matchingEventLocation.TitleJP, matchingEventLocation.Depth, matchingEventLocation.MinDepth, mapIdsJson)
				if err != nil {
					return gameLocation, err
				}

				locationId, err := res.LastInsertId()
				if err != nil {
					return gameLocation, err
				}

				gameLocation = &GameLocation{
					Id:     int(locationId),
					Game:   config.gameName,
					Name:   matchingEventLocation.Title,
					MapIds: matchingEventLocation.MapIds,
				}

				return gameLocation, nil
			}
		}

		return gameLocation, err
	}

	err = json.Unmarshal([]byte(mapIdsJson), &gameLocation.MapIds)
	if err != nil {
		return gameLocation, err
	}

	return gameLocation, nil
}

func writePlayerGameLocation(uuid string, locationName string) error {
	_, err := db.Exec("INSERT IGNORE INTO playerGameLocations (uuid, locationId, timestamp) (SELECT ?, gl.id, UTC_TIMESTAMP() FROM gameLocations gl WHERE gl.title = ? AND gl.game = ? LIMIT 1)", uuid, locationName, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func getPlayerGameLocationIds(uuid string, gameId string) (gameLocationIds []int, err error) {
	var locationIds []int

	results, err := db.Query("SELECT gl.id FROM playerGameLocations pgl JOIN gameLocations gl ON gl.id = pgl.locationId AND gl.game = ? WHERE pgl.uuid = ?", gameId, uuid)
	if err != nil {
		return gameLocationIds, err
	}

	defer results.Close()

	for results.Next() {
		var locationId int
		err = results.Scan(&locationId)
		if err != nil {
			return gameLocationIds, err
		}

		locationIds = append(locationIds, locationId)
	}

	return locationIds, nil
}

func getPlayerGameLocationCompletion(uuid string, gameId string) (gameLocationCompletion int, err error) {
	err = db.QueryRow("SELECT FLOOR(COUNT(*) / (SELECT COUNT(*) FROM gameLocations WHERE game = ? AND secret = 0) * 100) FROM playerGameLocations pgl JOIN gameLocations gl ON gl.id = pgl.locationId WHERE gl.game = ? and gl.secret = 0 AND pgl.uuid = ?", gameId, gameId, uuid).Scan(&gameLocationCompletion)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return gameLocationCompletion, err
	}

	return gameLocationCompletion, nil
}

func getPlayerMissingGameLocationNames(uuid string, locationNames []string) ([]string, error) {
	var missingGameLocationNames []string

	if len(locationNames) == 0 {
		return missingGameLocationNames, nil
	}

	var queryArgs []any
	queryArgs = append(queryArgs, config.gameName)

	for _, locationName := range locationNames {
		queryArgs = append(queryArgs, locationName)
	}

	queryArgs = append(queryArgs, uuid)

	results, err := db.Query("SELECT gl.title FROM gameLocations gl WHERE gl.game = ? AND gl.title IN (?"+strings.Repeat(", ?", len(locationNames)-1)+") AND NOT EXISTS (SELECT * FROM playerGameLocations pgl WHERE pgl.uuid = ? AND pgl.locationId = gl.id)", queryArgs...)
	if err != nil {
		return missingGameLocationNames, err
	}

	defer results.Close()

	for results.Next() {
		var locationName string
		err = results.Scan(&locationName)
		if err != nil {
			return missingGameLocationNames, err
		}

		missingGameLocationNames = append(missingGameLocationNames, locationName)
	}

	return missingGameLocationNames, nil
}

func getPlayerAllMissingGameLocationNames(uuid string) ([]string, error) {
	var missingGameLocationNames []string

	results, err := db.Query("SELECT gl.title FROM gameLocations gl WHERE gl.game = ? AND gl.secret = 0 AND NOT EXISTS (SELECT * FROM playerGameLocations pgl WHERE pgl.uuid = ? AND pgl.locationId = gl.id)", config.gameName, uuid)
	if err != nil {
		return missingGameLocationNames, err
	}

	defer results.Close()

	for results.Next() {
		var locationName string
		err = results.Scan(&locationName)
		if err != nil {
			return missingGameLocationNames, err
		}

		missingGameLocationNames = append(missingGameLocationNames, locationName)
	}

	return missingGameLocationNames, nil
}

func setCurrentEventPeriodId() error {
	var periodId int

	err := db.QueryRow("SELECT id FROM eventPeriods WHERE UTC_DATE() >= startDate AND UTC_DATE() < endDate").Scan(&periodId)
	if err != nil {
		currentEventPeriodId = 0
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	currentEventPeriodId = periodId

	return nil
}

func getCurrentEventPeriodData() (eventPeriod EventPeriod, err error) {
	err = db.QueryRow("SELECT ep.periodOrdinal, ep.endDate, gep.enableVms FROM eventPeriods ep JOIN gameEventPeriods gep ON gep.periodId = ep.id AND gep.game = ? WHERE UTC_DATE() >= ep.startDate AND UTC_DATE() < ep.endDate", config.gameName).Scan(&eventPeriod.PeriodOrdinal, &eventPeriod.EndDate, &eventPeriod.EnableVms)
	if err != nil {
		eventPeriod.PeriodOrdinal = -1
		if err == sql.ErrNoRows {
			return eventPeriod, nil
		}
		return eventPeriod, err
	}

	return eventPeriod, nil
}

func getGameCurrentEventPeriodsData() (gameEventPeriods map[string]*EventPeriod, err error) {
	gameEventPeriods = make(map[string]*EventPeriod)

	results, err := db.Query("SELECT gep.id, ep.periodOrdinal, ep.endDate, gep.enableVms, gep.game FROM eventPeriods ep JOIN gameEventPeriods gep ON gep.periodId = ep.id WHERE UTC_DATE() >= ep.startDate AND UTC_DATE() < ep.endDate")
	if err != nil {
		return gameEventPeriods, err
	}

	defer results.Close()

	for results.Next() {
		var gameId string
		var eventPeriod EventPeriod

		err = results.Scan(&eventPeriod.Id, &eventPeriod.PeriodOrdinal, &eventPeriod.EndDate, &eventPeriod.EnableVms, &gameId)
		if err != nil {
			return gameEventPeriods, err
		}

		gameEventPeriods[gameId] = &eventPeriod
	}

	return gameEventPeriods, nil
}

func setCurrentGameEventPeriodId() error {
	var gamePeriodId int

	err := db.QueryRow("SELECT id FROM gameEventPeriods WHERE game = ? AND periodId = ?", config.gameName, currentEventPeriodId).Scan(&gamePeriodId)
	if err != nil {
		currentGameEventPeriodId = 0
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	currentGameEventPeriodId = gamePeriodId

	return nil
}

func getRandomGameForEventLocation(pool map[string][]*EventLocationData, eventLocationCountThreshold int) (gameId string, err error) {
	results, err := db.Query("SELECT CEIL(AVG(gpc.playerCount)), gpc.game FROM gamePlayerCounts gpc JOIN gameEventPeriods gep ON gep.periodId = ? AND gep.game = gpc.game GROUP BY gpc.game", currentEventPeriodId)
	if err != nil {
		return "", err
	}

	defer results.Close()

	var playerCounts []int
	var gameIds []string
	var totalPlayerCount int

	for results.Next() {
		var currentPlayerCount int
		var currentGameId string

		err = results.Scan(&currentPlayerCount, &currentGameId)
		if err != nil {
			return "", err
		}

		// Ignore games with no event locations in the current pool
		if eventLocations, ok := pool[currentGameId]; currentGameId != "2kki" && (!ok || len(eventLocations) < eventLocationCountThreshold) {
			continue
		}

		if currentPlayerCount == 0 {
			currentPlayerCount = 1
		}

		totalPlayerCount += currentPlayerCount

		playerCounts = append(playerCounts, currentPlayerCount)
		gameIds = append(gameIds, currentGameId)
	}

	avgPlayerCount := int(math.Ceil(float64(totalPlayerCount) / float64(len(gameIds))))

	var poolThresholds []int
	gamePool := make(map[int]string)
	var totalPoolValue int

	poolCommonValue := int(math.Ceil(float64(avgPlayerCount) * gameEventShareFactor))

	for i, count := range playerCounts {
		poolValue := int(math.Floor(float64(count)*(1-gameEventShareFactor))) + poolCommonValue
		poolThreshold := poolValue + totalPoolValue

		gamePool[poolThreshold] = gameIds[i]
		poolThresholds = append(poolThresholds, poolThreshold)

		totalPoolValue += poolValue
	}

	randValue := rand.Intn(totalPoolValue)

	for _, value := range poolThresholds {
		if randValue < value {
			gameId = gamePool[value]
			break
		}
	}

	return gameId, nil
}

func getPlayerEventExpData(playerUuid string) (eventExp EventExp, err error) {
	totalEventExp, err := getPlayerTotalEventExp(playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.TotalExp = totalEventExp

	periodEventExp, err := getPlayerPeriodEventExp(playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.PeriodExp = periodEventExp

	weekEventExp, err := getPlayerWeekEventExp(playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.WeekExp = weekEventExp

	return eventExp, nil
}

func getPlayerTotalEventExp(playerUuid string) (totalEventExp int, err error) {
	err = db.QueryRow("SELECT SUM(exp) FROM ((SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN gameEventPeriods gep ON gep.id = el.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ec.uuid = ?) UNION ALL (SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventVms ev ON ev.id = ec.eventId AND ec.type = 2 JOIN gameEventPeriods gep ON gep.id = ev.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ec.uuid = ?)) eventExp", playerUuid, playerUuid).Scan(&totalEventExp)
	if err != nil {
		return totalEventExp, err
	}

	return totalEventExp, nil
}

func getPlayerPeriodEventExp(playerUuid string) (periodEventExp int, err error) {
	err = db.QueryRow("SELECT SUM(exp) FROM ((SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN gameEventPeriods gep ON gep.id = el.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ep.id = ? AND ec.uuid = ?) UNION ALL (SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventVms ev ON ev.id = ec.eventId AND ec.type = 2 JOIN gameEventPeriods gep ON gep.id = ev.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ep.id = ? AND ec.uuid = ?)) eventExp", currentEventPeriodId, playerUuid, currentEventPeriodId, playerUuid).Scan(&periodEventExp)
	if err != nil {
		return periodEventExp, err
	}

	return periodEventExp, nil
}

func getPlayerWeekEventExp(playerUuid string) (weekEventExp int, err error) {
	weekdayIndex := int(time.Now().UTC().Weekday())

	err = db.QueryRow("SELECT SUM(exp) FROM ((SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN gameEventPeriods gep ON gep.id = el.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ep.id = ? AND ec.uuid = ? AND DATE_SUB(UTC_DATE(), INTERVAL ? DAY) <= el.startDate AND DATE_ADD(UTC_DATE(), INTERVAL ? DAY) >= el.endDate) UNION ALL (SELECT COALESCE(SUM(ec.exp), 0) exp FROM eventCompletions ec JOIN eventVms ev ON ev.id = ec.eventId AND ec.type = 2 JOIN gameEventPeriods gep ON gep.id = ev.gamePeriodId JOIN eventPeriods ep ON ep.id = gep.periodId WHERE ep.id = ? AND ec.uuid = ? AND DATE_SUB(UTC_DATE(), INTERVAL ? DAY) <= ev.startDate AND DATE_ADD(UTC_DATE(), INTERVAL ? DAY) >= ev.endDate)) eventExp", currentEventPeriodId, playerUuid, weekdayIndex, 7-weekdayIndex, currentEventPeriodId, playerUuid, weekdayIndex, 7-weekdayIndex).Scan(&weekEventExp)
	if err != nil {
		return weekEventExp, err
	}

	return weekEventExp, nil
}

func getPlayerEventLocationCount(playerUuid string) (eventLocationCount int, err error) {
	err = db.QueryRow("SELECT COUNT(eventId) FROM eventCompletions WHERE uuid = ? AND type < 2", playerUuid).Scan(&eventLocationCount)
	if err != nil {
		return eventLocationCount, err
	}

	return eventLocationCount, nil
}

func getPlayerEventLocationCompletion(playerUuid string) (eventLocationCompletion int, err error) {
	// Relies on rankings but is much faster than calculating directly
	err = db.QueryRow("SELECT FLOOR(valueFloat * 100) FROM rankingEntries WHERE uuid = ? AND categoryId = 'eventLocationCompletion' AND subCategoryId = 'all'", playerUuid).Scan(&eventLocationCompletion)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return eventLocationCompletion, err
	}

	return eventLocationCompletion, nil
}

func getLocationName(locationId int) (locationName string, err error) {
	err = db.QueryRow("SELECT l.title FROM gameLocations l WHERE l.id = ?", locationId).Scan(&locationName)
	if err != nil {
		return "", err
	}

	return locationName, nil
}

func getOrWriteLocationIdForEventLocation(gameId string, gameEventPeriodId int, title string, titleJP string, depth int, minDepth int, mapIds []string) (locationId int, err error) {
	mapIdsJson, err := json.Marshal(mapIds)
	if err != nil {
		return locationId, err
	}

	_, err = db.Exec("INSERT INTO gameLocations (game, title, titleJP, depth, minDepth, mapIds) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE titleJP = ?, depth = ?, minDepth = ?, mapIds = ?", gameId, title, titleJP, depth, minDepth, mapIdsJson, titleJP, depth, minDepth, mapIdsJson)
	if err != nil {
		return locationId, err
	}

	db.QueryRow("SELECT l.id FROM gameLocations l JOIN gameEventPeriods gep ON gep.game = l.game WHERE gep.id = ? AND l.title = ?", gameEventPeriodId, title).Scan(&locationId)

	return locationId, nil
}

func getOrWriteLocationIdForPlayerEventLocation(gameId string, gameEventPeriodId int, playerUuid string, title string, titleJP string, depth int, minDepth int, mapIds []string) (locationId int, err error) {
	var playerEventLocationQueueLength int
	db.QueryRow("SELECT COUNT(*) FROM playerEventLocationQueue WHERE game = ? AND date = UTC_DATE()", gameId).Scan(&playerEventLocationQueueLength)

	if playerEventLocationQueueLength > 0 {
		var currentPlayerEventLocationQueueLength int
		db.QueryRow("SELECT COUNT(*) FROM eventCompletions ec JOIN playerEventLocations pel ON pel.id = ec.eventId AND ec.type = 1 WHERE pel.gamePeriodId = ? AND pel.startDate = UTC_DATE() AND pel.uuid = ?", gameEventPeriodId, playerUuid).Scan(&currentPlayerEventLocationQueueLength)

		if currentPlayerEventLocationQueueLength < playerEventLocationQueueLength {
			db.QueryRow("SELECT locationId FROM playerEventLocationQueue WHERE game = ? AND date = UTC_DATE() AND queueIndex = ?", gameId, currentPlayerEventLocationQueueLength+1).Scan(&locationId)

			return locationId, nil
		}
	}

	locationId, err = getOrWriteLocationIdForEventLocation(gameId, gameEventPeriodId, title, titleJP, depth, minDepth, mapIds)
	if err != nil {
		return locationId, err
	}

	_, err = db.Exec("INSERT INTO playerEventLocationQueue (game, date, queueIndex, locationId) VALUES (?, UTC_DATE(), ?, ?)", gameId, playerEventLocationQueueLength+1, locationId)
	if err != nil {
		return locationId, err
	}

	return locationId, nil
}

func writeEventLocationData(gameId string, gameEventPeriodId int, eventType int, title string, titleJP string, depth int, minDepth int, exp int, mapIds []string) error {
	var days int
	var offsetDays int
	weekday := time.Now().UTC().Weekday()
	switch eventType {
	case 0:
		days = 1
	case 1:
		days = 7
		offsetDays = int(weekday)
	default:
		if weekday == time.Friday || weekday == time.Saturday {
			days = 2
			offsetDays = int(weekday - time.Friday)
		} else {
			return nil
		}
	}

	days -= offsetDays

	locationId, err := getOrWriteLocationIdForEventLocation(gameId, gameEventPeriodId, title, titleJP, depth, minDepth, mapIds)
	if err != nil {
		return err
	}

	_, err = db.Exec("INSERT INTO eventLocations (locationId, gamePeriodId, type, exp, startDate, endDate) VALUES (?, ?, ?, ?, DATE_SUB(UTC_DATE(), INTERVAL ? DAY), DATE_ADD(UTC_DATE(), INTERVAL ? DAY))", locationId, gameEventPeriodId, eventType, exp, offsetDays, days)
	if err != nil {
		return err
	}

	return nil
}

func writePlayerEventLocationData(gameId string, gameEventPeriodId int, playerUuid string, title string, titleJP string, depth int, minDepth int, mapIds []string) error {
	locationId, err := getOrWriteLocationIdForPlayerEventLocation(gameId, gameEventPeriodId, playerUuid, title, titleJP, depth, minDepth, mapIds)
	if err != nil {
		return err
	}

	_, err = db.Exec("INSERT INTO playerEventLocations (locationId, gamePeriodId, uuid, startDate, endDate) SELECT ?, ?, ?, UTC_DATE(), DATE_ADD(UTC_DATE(), INTERVAL 1 DAY) WHERE NOT EXISTS(SELECT * FROM playerEventLocations pel LEFT JOIN eventCompletions ec ON ec.eventId = pel.id AND ec.type = 1 AND ec.uuid = pel.uuid WHERE pel.uuid = ? AND pel.gamePeriodId = ? AND ec.uuid IS NULL AND UTC_DATE() >= pel.startDate AND UTC_DATE() < pel.endDate)", locationId, gameEventPeriodId, playerUuid, playerUuid, gameEventPeriodId)
	if err != nil {
		return err
	}

	return nil
}

func getCurrentPlayerEventLocationsData(playerUuid string) (eventLocations []*EventLocation, err error) {
	results, err := db.Query("SELECT el.id, el.type, gep.game, l.id, l.title, l.titleJP, l.depth, l.minDepth, el.exp, el.endDate, CASE WHEN ec.uuid IS NOT NULL THEN 1 ELSE 0 END FROM eventLocations el JOIN gameLocations l ON l.id = el.locationId JOIN gameEventPeriods gep ON gep.id = el.gamePeriodId LEFT JOIN eventCompletions ec ON ec.eventId = el.id AND ec.type = 0 AND ec.uuid = ? WHERE gep.periodId = ? AND UTC_DATE() >= el.startDate AND UTC_DATE() < el.endDate ORDER BY 2, 1", playerUuid, currentEventPeriodId)
	if err != nil {
		return eventLocations, err
	}

	defer results.Close()

	for results.Next() {
		var eventLocation EventLocation

		var completeBin int

		err := results.Scan(&eventLocation.Id, &eventLocation.Type, &eventLocation.Game, &eventLocation.LocationId, &eventLocation.Title, &eventLocation.TitleJP, &eventLocation.Depth, &eventLocation.MinDepth, &eventLocation.Exp, &eventLocation.EndDate, &completeBin)
		if err != nil {
			return eventLocations, err
		}

		if eventLocation.MinDepth == eventLocation.Depth {
			eventLocation.MinDepth = 0
		}

		if completeBin == 1 {
			eventLocation.Complete = true
		}

		eventLocations = append(eventLocations, &eventLocation)
	}

	results, err = db.Query("SELECT pel.id, gep.game, pl.id, pl.title, pl.titleJP, pl.depth, pl.minDepth, pel.endDate FROM playerEventLocations pel JOIN gameLocations pl ON pl.id = pel.locationId JOIN gameEventPeriods gep ON gep.id = pel.gamePeriodId LEFT JOIN eventCompletions ec ON ec.eventId = pel.id AND ec.type = 1 AND ec.uuid = pel.uuid WHERE pel.uuid = ? AND gep.periodId = ? AND gep.game = ? AND ec.uuid IS NULL AND UTC_DATE() >= pel.startDate AND UTC_DATE() < pel.endDate ORDER BY 1", playerUuid, currentEventPeriodId, config.gameName)
	if err != nil {
		return eventLocations, err
	}

	defer results.Close()

	for results.Next() {
		var eventLocation EventLocation

		err := results.Scan(&eventLocation.Id, &eventLocation.Game, &eventLocation.LocationId, &eventLocation.Title, &eventLocation.TitleJP, &eventLocation.Depth, &eventLocation.MinDepth, &eventLocation.EndDate)
		if err != nil {
			return eventLocations, err
		}

		eventLocation.Type = -1

		if eventLocation.MinDepth == eventLocation.Depth {
			eventLocation.MinDepth = 0
		}

		eventLocations = append(eventLocations, &eventLocation)
	}

	return eventLocations, nil
}

func tryCompleteEventLocation(playerUuid string, location string) (exp int, err error) {
	if client, ok := clients.Load(playerUuid); ok {
		if client.roomC == nil {
			return -1, err
		}

		// prevent race condition
		clientMapId := client.roomC.mapId

		results, err := db.Query("SELECT el.id, el.type, el.exp, l.mapIds FROM eventLocations el JOIN gameLocations l ON l.id = el.locationId WHERE el.gamePeriodId = ? AND l.title = ? AND UTC_DATE() >= el.startDate AND UTC_DATE() < el.endDate ORDER BY 2", currentGameEventPeriodId, location)
		if err != nil {
			return -1, err
		}

		defer results.Close()

		weekEventExp, err := getPlayerWeekEventExp(playerUuid)
		if err != nil {
			return -1, err
		}

		for results.Next() {
			var eventId string
			var eventType int
			var eventExp int
			var mapIdsJson string

			err := results.Scan(&eventId, &eventType, &eventExp, &mapIdsJson)
			if err != nil {
				return exp, err
			}

			var mapIds []string
			err = json.Unmarshal([]byte(mapIdsJson), &mapIds)
			if err != nil {
				return exp, err
			}

			for _, mapId := range mapIds {
				if clientMapId != mapId {
					continue
				}
				if weekEventExp >= weeklyExpCap {
					eventExp = 0
				} else if weekEventExp+eventExp > weeklyExpCap {
					eventExp = weeklyExpCap - weekEventExp
				}

				_, err = db.Exec("INSERT INTO eventCompletions (eventId, uuid, type, timestampCompleted, exp) VALUES (?, ?, 0, ?, ?)", eventId, playerUuid, time.Now(), eventExp)
				if err != nil {
					break
				}

				exp += eventExp
				weekEventExp += eventExp
				break
			}
		}

		return exp, nil
	}

	return -1, err
}

func tryCompletePlayerEventLocation(playerUuid string, location string) (success bool, err error) {
	if client, ok := clients.Load(playerUuid); ok {
		if client.roomC == nil {
			return false, err
		}

		// prevent race condition
		clientMapId := client.roomC.mapId

		results, err := db.Query("SELECT pel.id, pl.mapIds FROM playerEventLocations pel JOIN gameLocations pl ON pl.id = pel.locationId WHERE pel.gamePeriodId = ? AND pl.title = ? AND pel.uuid = ? AND UTC_DATE() >= pel.startDate AND UTC_DATE() < pel.endDate ORDER BY 2", currentGameEventPeriodId, location, playerUuid)
		if err != nil {
			return false, err
		}

		defer results.Close()

		for results.Next() {
			var eventId string
			var mapIdsJson string

			err := results.Scan(&eventId, &mapIdsJson)
			if err != nil {
				return false, err
			}

			var mapIds []string
			err = json.Unmarshal([]byte(mapIdsJson), &mapIds)
			if err != nil {
				return false, err
			}

			for _, mapId := range mapIds {
				if clientMapId != mapId {
					continue
				}

				_, err = db.Exec("INSERT INTO eventCompletions (eventId, uuid, type, timestampCompleted, exp) VALUES (?, ?, 1, ?, 0)", eventId, playerUuid, time.Now())
				if err != nil {
					break
				}

				success = true
				break
			}
		}

		return success, nil
	}

	return false, err
}

func getPlayerEventVmCount(playerUuid string) (eventVmCount int, err error) {
	err = db.QueryRow("SELECT COUNT(eventId) FROM eventCompletions WHERE uuid = ? AND type = 2", playerUuid).Scan(&eventVmCount)
	if err != nil {
		return eventVmCount, err
	}

	return eventVmCount, nil
}

func getCurrentPlayerEventVmsData(playerUuid string) (eventVms []*EventVm, err error) {
	results, err := db.Query("SELECT ev.id, gep.game, ev.exp, ev.endDate, CASE WHEN ec.uuid IS NOT NULL THEN 1 ELSE 0 END FROM eventVms ev JOIN gameEventPeriods gep ON gep.id = ev.gamePeriodId LEFT JOIN eventCompletions ec ON ec.eventId = ev.id AND ec.type = 2 AND ec.uuid = ? WHERE gep.periodId = ? AND UTC_DATE() >= ev.startDate AND UTC_DATE() < ev.endDate ORDER BY 2, 1", playerUuid, currentEventPeriodId)
	if err != nil {
		return eventVms, err
	}

	defer results.Close()

	for results.Next() {
		var eventVm EventVm

		var completeBin int

		err := results.Scan(&eventVm.Id, &eventVm.Game, &eventVm.Exp, &eventVm.EndDate, &completeBin)
		if err != nil {
			return eventVms, err
		}

		if completeBin == 1 {
			eventVm.Complete = true
		}

		eventVms = append(eventVms, &eventVm)
	}

	return eventVms, nil
}

func getEventVmInfo(id int) (mapId int, eventId int, err error) {
	err = db.QueryRow("SELECT mapId, eventId FROM eventVms WHERE id = ?", id).Scan(&mapId, &eventId)
	if err != nil {
		return 0, 0, err
	}

	return mapId, eventId, nil
}

func writeEventVmData(mapId int, eventId int, exp int) error {
	var days int
	var offsetDays int
	weekday := time.Now().UTC().Weekday()

	switch weekday {
	case time.Sunday, time.Monday:
		days = 2
		offsetDays = int(weekday)
	case time.Tuesday, time.Wednesday, time.Thursday:
		days = 3
		offsetDays = int(weekday - time.Tuesday)
	case time.Friday, time.Saturday:
		days = 2
		offsetDays = int(weekday - time.Friday)
	}

	days -= offsetDays

	_, err := db.Exec("INSERT INTO eventVms (gamePeriodId, mapId, eventId, exp, startDate, endDate) VALUES (?, ?, ?, ?, DATE_SUB(UTC_DATE(), INTERVAL ? DAY), DATE_ADD(UTC_DATE(), INTERVAL ? DAY))", currentGameEventPeriodId, mapId, eventId, exp, offsetDays, days)
	if err != nil {
		return err
	}

	return nil
}

func tryCompleteEventVm(playerUuid string, mapId int, eventId int) (exp int, err error) {
	if client, ok := clients.Load(playerUuid); ok {
		if client.roomC == nil {
			return -1, err
		}

		// prevent race condition
		clientMapId := client.roomC.mapId

		results, err := db.Query("SELECT ev.id, ev.mapId, ev.eventId, ev.exp FROM eventVms ev JOIN gameEventPeriods gep ON gep.id = ev.gamePeriodId WHERE gep.periodId = ? AND ev.mapId = ? AND ev.eventId = ? AND UTC_DATE() >= ev.startDate AND UTC_DATE() < ev.endDate ORDER BY 2", currentEventPeriodId, mapId, eventId)
		if err != nil {
			return -1, err
		}

		defer results.Close()

		currentEventVmsData, err := getCurrentPlayerEventVmsData(playerUuid)
		if err != nil {
			return -1, err
		}

		weekEventExp, err := getPlayerWeekEventExp(playerUuid)
		if err != nil {
			return -1, err
		}

		for results.Next() {
			var eventId int
			var eventMapId int
			var eventEvId int
			var eventExp int

			err := results.Scan(&eventId, &eventMapId, &eventEvId, &eventExp)
			if err != nil {
				return exp, err
			}

			for _, eventVm := range currentEventVmsData {
				if eventVm.Id == eventId {
					if eventVm.Complete {
						return -1, nil
					}
					break
				}
			}

			if clientMapId != fmt.Sprintf("%04d", eventMapId) {
				continue
			}
			if weekEventExp >= weeklyExpCap {
				eventExp = 0
			} else if weekEventExp+eventExp > weeklyExpCap {
				eventExp = weeklyExpCap - weekEventExp
			}

			_, err = db.Exec("INSERT INTO eventCompletions (eventId, uuid, type, timestampCompleted, exp) VALUES (?, ?, 2, ?, ?)", eventId, playerUuid, time.Now(), eventExp)
			if err != nil {
				break
			}

			exp += eventExp
			weekEventExp += eventExp
		}

		return exp, nil
	}

	return -1, err
}

func getPlayerTags(playerUuid string) (tags []string, lastUnlocked time.Time, err error) {
	results, err := db.Query("SELECT name, timestampUnlocked FROM playerTags WHERE uuid = ?", playerUuid)
	if err != nil {
		return tags, lastUnlocked, err
	}

	defer results.Close()

	for results.Next() {
		var tagName string
		var timestamp time.Time
		err := results.Scan(&tagName, &timestamp)
		if err != nil {
			return tags, lastUnlocked, err
		}
		tags = append(tags, tagName)
		if timestamp.After(lastUnlocked) {
			lastUnlocked = timestamp
		}
	}

	return tags, lastUnlocked, nil
}

func tryWritePlayerTag(playerUuid string, name string) (success bool, err error) {
	if client, ok := clients.Load(playerUuid); ok { // Player must be online to add a tag
		if client.roomC == nil {
			return false, nil
		}

		// prevent race condition
		tags := client.roomC.tags

		// Spare SQL having to deal with a duplicate record by checking player tags beforehand
		var tagExists bool
		for _, tag := range tags {
			if tag == name {
				tagExists = true
				break
			}
		}
		if !tagExists {
			_, err = db.Exec("INSERT INTO playerTags (uuid, name, timestampUnlocked) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE name = name", playerUuid, name, time.Now())
			if err != nil {
				return false, err
			}
			return true, nil
		}
	}

	return false, nil
}

func getPlayerTimeTrialRecords(playerUuid string) (timeTrialRecords []*TimeTrialRecord, err error) {
	results, err := db.Query("SELECT mapId, MIN(seconds) FROM playerTimeTrials WHERE uuid = ? GROUP BY mapId", playerUuid)
	if err != nil {
		return timeTrialRecords, err
	}

	defer results.Close()

	for results.Next() {
		var timeTrialRecord TimeTrialRecord

		err := results.Scan(&timeTrialRecord.MapId, &timeTrialRecord.Seconds)
		if err != nil {
			return timeTrialRecords, err
		}

		timeTrialRecords = append(timeTrialRecords, &timeTrialRecord)
	}

	return timeTrialRecords, nil
}

func tryWritePlayerTimeTrial(playerUuid string, mapId int, seconds int) (success bool, err error) {
	var prevSeconds int
	err = db.QueryRow("SELECT seconds FROM playerTimeTrials WHERE uuid = ? AND mapId = ?", playerUuid, mapId).Scan(&prevSeconds)
	if err != nil {
		if err != sql.ErrNoRows {
			return false, err
		}
	} else if seconds >= prevSeconds {
		return false, nil
	} else {
		_, err = db.Exec("UPDATE playerTimeTrials SET seconds = ?, timestampCompleted = ? WHERE uuid = ? AND mapId = ?", seconds, time.Now(), playerUuid, mapId)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	_, err = db.Exec("INSERT INTO playerTimeTrials (uuid, mapId, seconds, timestampCompleted) VALUES (?, ?, ?, ?)", playerUuid, mapId, seconds, time.Now())
	if err != nil {
		return false, err
	}

	return true, nil
}

func getBannedMutedPlayers(banned bool) (players []PlayerInfo) {
	var actionStr string

	if banned {
		actionStr = "banned"
	} else {
		actionStr = "muted"
	}

	results, err := db.Query("SELECT uuid, rank FROM players WHERE " + actionStr + " = 1")
	if err != nil {
		return players
	}

	defer results.Close()

	for results.Next() {
		var uuid string
		var rank int

		err := results.Scan(&uuid, &rank)
		if err != nil {
			return players
		}

		players = append(players, PlayerInfo{
			Uuid: uuid,
			Name: getNameFromUuid(uuid),
			Rank: rank,
		})
	}

	return players
}

func getUuidFromName(name string) (uuid string, err error) {
	err = db.QueryRow("SELECT uuid FROM accounts WHERE user = ?", name).Scan(&uuid)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	return uuid, nil
}

func getNameFromUuid(uuid string) (name string) {
	// get name from sessionClients if they're connected
	if client, ok := clients.Load(uuid); ok {
		return client.name
	}

	// otherwise check accounts
	err := db.QueryRow("SELECT user FROM accounts WHERE uuid = ?", uuid).Scan(&name)
	if err != nil {
		return ""
	}

	// if we got a name then return it
	if name != "" {
		return name
	}

	// otherwise check playergamedata
	err = db.QueryRow("SELECT name FROM playerGameData WHERE uuid = ?").Scan(&name)
	if err != nil {
		return ""
	}

	return name
}

func isIpBanned(ip string) bool {
	var banned int

	// check if account is banned
	err := db.QueryRow("SELECT banned FROM players JOIN accounts ON players.uuid = accounts.uuid WHERE accounts.ip = ?", ip).Scan(&banned)
	if err != nil && err != sql.ErrNoRows {
		return false
	}

	if banned == 1 {
		return true
	}

	// check if guest account is banned
	err = db.QueryRow("SELECT banned FROM players WHERE ip = ?", ip).Scan(&banned)
	if err != nil {
		return false
	}

	return banned == 1
}

func getUuidFromToken(token string) (uuid string) {
	db.QueryRow("SELECT uuid FROM playerSessions WHERE sessionId = ? AND NOW() < expiration", token).Scan(&uuid)

	return uuid
}

func writeGamePlayerCount(playerCount int) error {
	_, err := db.Exec("INSERT INTO gamePlayerCounts (game, playerCount) VALUES (?, ?)", config.gameName, playerCount)
	if err != nil {
		return err
	}

	var playerCounts int
	err = db.QueryRow("SELECT COUNT(*) FROM gamePlayerCounts WHERE game = ?", config.gameName).Scan(&playerCounts)
	if err != nil {
		return err
	}

	if playerCounts > 28 {
		_, err = db.Exec("DELETE FROM gamePlayerCounts WHERE game = ? ORDER BY id LIMIT ?", config.gameName, playerCounts-28)
		if err != nil {
			return err
		}
	}

	return nil
}

func getReportersForPlayer(targetUuid, msgId string) (result map[string]string, err error) {
	rows, err := db.Query(`
		SELECT pgd.name, pr.reason FROM playerReports pr
		JOIN playerGameData pgd ON pgd.uuid = pr.uuid
		WHERE pr.targetUuid = ? AND pr.msgId = ? AND NOT actionTaken`, targetUuid, msgId)
	if err != nil {
		return result, err
	}

	result = make(map[string]string)
	defer rows.Close()
	for rows.Next() {
		var reporter string
		var reason string
		err = rows.Scan(&reporter, &reason)
		if err != nil {
			return nil, err
		}

		result[reporter] = reason
	}

	return result, nil
}

func doCleanupQueries() error {
	// Remove player records with no game activity
	_, err := db.Exec("DELETE IGNORE FROM players WHERE ip IS NOT NULL")
	if err != nil {
		return err
	}

	// Remove player sessions that have expired
	_, err = db.Exec("DELETE FROM playerSessions WHERE expiration < NOW()")
	if err != nil {
		return err
	}

	// Remove player expeditions that were never completed
	_, err = db.Exec("DELETE pel FROM playerEventLocations pel WHERE UTC_DATE() > pel.endDate AND NOT EXISTS (SELECT ec.eventId FROM eventCompletions ec WHERE ec.eventId = pel.id AND ec.type = 1)")
	if err != nil {
		return err
	}

	// Remove player event location queue for past dates
	_, err = db.Exec("DELETE FROM playerEventLocationQueue WHERE UTC_DATE() > date")
	if err != nil {
		return err
	}

	// Remove Yume 2kki Explorer API query cache records that have expired
	_, err = db.Exec("DELETE FROM 2kkiApiQueries WHERE timestampExpired < NOW()")
	if err != nil {
		return err
	}

	return nil
}
