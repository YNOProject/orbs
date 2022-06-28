package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/thanhpk/randstr"
)

var (
	db *sql.DB
)

func setDatabase() {
	conn, err := sql.Open("mysql", config.dbUser+":"+config.dbPass+"@tcp("+config.dbHost+")/"+config.dbName+"?parseTime=true")
	if err != nil {
		return
	}

	db = conn
}

func readPlayerData(ip string) (uuid string, rank int, banned bool, muted bool) {
	err := db.QueryRow("SELECT uuid, rank, banned, muted FROM players WHERE ip = ?", ip).Scan(&uuid, &rank, &banned, &muted)
	if err != nil {
		if err == sql.ErrNoRows {
			uuid = randstr.String(16)
			banned = isVpn(ip)
			createPlayerData(ip, uuid, 0, banned)
		} else {
			return "", 0, false, false
		}
	}

	return uuid, rank, banned, muted
}

func readPlayerDataFromToken(token string) (uuid string, name string, rank int, badge string, banned bool, muted bool) {
	err := db.QueryRow("SELECT a.uuid, a.user, pd.rank, a.badge, pd.banned, pd.muted FROM accounts a JOIN playerSessions ps ON ps.uuid = a.uuid JOIN players pd ON pd.uuid = a.uuid WHERE ps.sessionId = ? AND NOW() < ps.expiration", token).Scan(&uuid, &name, &rank, &badge, &banned, &muted)
	if err != nil {
		return "", "", 0, "", false, false
	}

	return uuid, name, rank, badge, banned, muted
}

func readPlayerRank(uuid string) (rank int) {
	if client, ok := sessionClients[uuid]; ok {
		return client.rank //return rank from session if client is connected
	}

	err := db.QueryRow("SELECT rank FROM players WHERE uuid = ?", uuid).Scan(&rank)
	if err != nil {
		return 0
	}

	return rank
}

func tryBanPlayer(senderUuid string, recipientUuid string) error { //called by api only
	if readPlayerRank(senderUuid) <= readPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-ban")
	}

	_, err := db.Exec("UPDATE players SET banned = 1 WHERE uuid = ?", recipientUuid)
	if err != nil {
		return err
	}

	if client, ok := hubClients[recipientUuid]; ok { //unregister client and close connection
		client.hub.unregister <- client
	}

	if client, ok := sessionClients[recipientUuid]; ok { //do the same for session
		session.unregister <- client
	}

	return nil
}

func tryMutePlayer(senderUuid string, recipientUuid string) error { //called by api only
	if readPlayerRank(senderUuid) <= readPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-mute")
	}

	_, err := db.Exec("UPDATE players SET muted = 1 WHERE uuid = ?", recipientUuid)
	if err != nil {
		return err
	}

	if client, ok := sessionClients[recipientUuid]; ok { //mute client if they're connected
		client.muted = true
	}

	return nil
}

func tryUnmutePlayer(senderUuid string, recipientUuid string) error { //called by api only
	if readPlayerRank(senderUuid) <= readPlayerRank(recipientUuid) {
		return errors.New("insufficient rank")
	}

	if senderUuid == recipientUuid {
		return errors.New("attempted self-unmute")
	}

	_, err := db.Exec("UPDATE players SET muted = 0 WHERE uuid = ?", recipientUuid)
	if err != nil {
		return err
	}

	if client, ok := sessionClients[recipientUuid]; ok { //unmute client if they're connected
		client.muted = false
	}

	return nil
}

func createPlayerData(ip string, uuid string, rank int, banned bool) error {
	_, err := db.Exec("INSERT INTO players (ip, uuid, rank, banned) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE uuid = ?, rank = ?, banned = ?", ip, uuid, rank, banned, uuid, rank, banned)
	if err != nil {
		return err
	}

	return nil
}

func readPlayerGameData(uuid string) (systemName string, spriteName string, spriteIndex int) {
	err := db.QueryRow("SELECT pgd.systemName, pgd.spriteName, pgd.spriteIndex FROM players pd LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid WHERE pd.uuid = ? AND pgd.game = ?", uuid, config.gameName).Scan(&systemName, &spriteName, &spriteIndex)
	if err != nil {
		return "", "", 0
	}

	return systemName, spriteName, spriteIndex
}

func updatePlayerGameData(client *SessionClient) error {
	_, err := db.Exec("INSERT INTO playerGameData (uuid, game, name, systemName, spriteName, spriteIndex) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE name = ?, systemName = ?, spriteName = ?, spriteIndex = ?", client.uuid, config.gameName, client.name, client.systemName, client.spriteName, client.spriteIndex, client.name, client.systemName, client.spriteName, client.spriteIndex)
	if err != nil {
		return err
	}

	return nil
}

func readPlayerInfo(ip string) (uuid string, name string, rank int) {
	err := db.QueryRow("SELECT pd.uuid, pgd.name, pd.rank FROM players pd LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid WHERE pd.ip = ? AND (pgd.uuid IS NULL OR pgd.game = ?)", ip, config.gameName).Scan(&uuid, &name, &rank)
	if err != nil {
		return "", "", 0
	}

	return uuid, name, rank
}

func readPlayerInfoFromToken(token string) (uuid string, name string, rank int, badge string, badgeSlotRows int, badgeSlotCols int) {
	err := db.QueryRow("SELECT a.uuid, a.user, pd.rank, a.badge, a.badgeSlotRows, a.badgeSlotCols FROM accounts a JOIN playerSessions ps ON ps.uuid = a.uuid JOIN players pd ON pd.uuid = a.uuid WHERE ps.sessionId = ? AND NOW() < ps.expiration", token).Scan(&uuid, &name, &rank, &badge, &badgeSlotRows, &badgeSlotCols)
	if err != nil {
		return "", "", 0, "", 0, 0
	}

	return uuid, name, rank, badge, badgeSlotRows, badgeSlotCols
}

func readPlayerBadgeSlotCounts(playerName string) (badgeSlotRows int, badgeSlotCols int) {
	err := db.QueryRow("SELECT badgeSlotRows, badgeSlotCols FROM accounts WHERE user = ?", playerName).Scan(&badgeSlotRows, &badgeSlotCols)
	if err != nil {
		return 1, 3
	}

	return badgeSlotRows, badgeSlotCols
}

func updatePlayerBadgeSlotCounts(uuid string) (err error) {
	query := "UPDATE accounts JOIN (SELECT pb.uuid, SUM(b.bp) bp, COUNT(b.badgeId) bc FROM playerBadges pb JOIN badges b ON b.badgeId = pb.badgeId GROUP BY pb.uuid) AS pb ON pb.uuid = accounts.uuid SET badgeSlotRows = CASE WHEN bp < 1000 THEN 1 WHEN bp < 2500 THEN 2 WHEN bp < 5000 THEN 3 WHEN bp < 10000 THEN 4 WHEN bp < 17500 THEN 5 WHEN bp < 30000 THEN 6 ELSE 7 END, badgeSlotCols = CASE WHEN bc < 50 THEN 3 WHEN bc < 150 THEN 4 WHEN bc < 300 THEN 5 WHEN bc < 500 THEN 6 ELSE 7 END"
	if uuid == "" {
		_, err = db.Exec(query)
	} else {
		query += " WHERE accounts.uuid = ?"
		_, err = db.Exec(query, uuid)
	}
	if err != nil {
		return err
	}

	return nil
}

func setPlayerBadge(uuid string, badge string) (err error) {
	if client, ok := sessionClients[uuid]; ok {
		client.badge = badge
	}

	_, err = db.Exec("UPDATE accounts SET badge = ? WHERE uuid = ?", badge, uuid)
	if err != nil {
		return err
	}

	return nil
}

func readPlayerBadgeSlots(playerName string, badgeSlotRows int, badgeSlotCols int) (badgeSlots [][]string, err error) {
	results, err := db.Query("SELECT pb.badgeId, pb.slotRow, pb.slotCol FROM playerBadges pb JOIN accounts a ON a.uuid = pb.uuid WHERE a.user = ? AND pb.slotRow BETWEEN 1 AND ? AND pb.slotCol BETWEEN 1 AND ? ORDER BY pb.slotRow, pb.slotCol", playerName, badgeSlotRows, badgeSlotCols)
	if err != nil {
		return badgeSlots, err
	}

	defer results.Close()

	var badgeId string
	var badgeRow int
	var badgeCol int

	for r := 1; r <= badgeSlotRows; r++ {
		var badgeSlotRow []string
		for c := 1; c <= badgeSlotCols; c++ {
			if badgeRow > r || (badgeRow == r && badgeCol >= c) {
				if badgeRow == r && badgeCol == c {
					badgeSlotRow = append(badgeSlotRow, badgeId)
				} else {
					badgeSlotRow = append(badgeSlotRow, "null")
				}
			} else {
				for {
					if !results.Next() {
						break
					}
					err := results.Scan(&badgeId, &badgeRow, &badgeCol)
					if err != nil {
						break
					}

					if badgeRow > r || (badgeRow == r && badgeCol >= c) {
						if badgeRow == r && badgeCol == c {
							badgeSlotRow = append(badgeSlotRow, badgeId)
						}
						break
					}
				}
				if len(badgeSlotRow) < c {
					badgeSlotRow = append(badgeSlotRow, "null")
				}
			}
		}
		badgeSlots = append(badgeSlots, badgeSlotRow)
	}

	return badgeSlots, nil
}

func setPlayerBadgeSlot(uuid string, badgeId string, slotRow int, slotCol int) (err error) {
	var slotCurrentBadgeId string
	err = db.QueryRow("SELECT badgeId FROM playerBadges WHERE uuid = ? AND slotRow = ? AND slotCol = ? LIMIT 1", uuid, slotRow, slotCol).Scan(&slotCurrentBadgeId)
	if err != nil {
		if err != sql.ErrNoRows {
			return err
		}
	} else if slotCurrentBadgeId == badgeId {
		return
	} else {
		if badgeId != "null" {
			var badgeCurrentSlotRow int
			var badgeCurrentSlotCol int
			err := db.QueryRow("SELECT slotRow, slotCol FROM playerBadges WHERE uuid = ? AND badgeId = ? LIMIT 1", uuid, badgeId).Scan(&badgeCurrentSlotRow, &badgeCurrentSlotCol)

			if err != nil && err != sql.ErrNoRows {
				return err
			} else {
				_, err = db.Exec("UPDATE playerBadges SET slotRow = ?, slotCol = ? WHERE uuid = ? AND badgeId = ?", badgeCurrentSlotRow, badgeCurrentSlotCol, uuid, slotCurrentBadgeId)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
			}
		} else {
			_, err = db.Exec("UPDATE playerBadges SET slotRow = 0, slotCol = 0 WHERE uuid = ? AND slotRow = ? AND slotCol = ?", uuid, slotRow, slotCol)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
		}
	}

	_, err = db.Exec("UPDATE playerBadges SET slotRow = ?, slotCol = ? WHERE uuid = ? AND badgeId = ?", slotRow, slotCol, uuid, badgeId)
	if err != nil {
		return err
	}

	return nil
}

func readPlayerPartyId(uuid string) (partyId int, err error) {
	err = db.QueryRow("SELECT pm.partyId FROM partyMembers pm JOIN parties p ON p.id = pm.partyId WHERE pm.uuid = ? AND p.game = ?", uuid, config.gameName).Scan(&partyId)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		} else {
			return 0, err
		}
	}

	return partyId, nil
}

func readAllPartyData(simple bool) (parties []*Party, err error) { //called by api only
	partyMembersByParty, err := readAllPartyMemberDataByParty(simple)
	if err != nil {
		return parties, err
	}

	results, err := db.Query("SELECT p.id, p.owner, p.name, p.public, p.pass, p.theme, p.description FROM parties p WHERE p.game = ?", config.gameName)
	if err != nil {
		return parties, err
	}

	defer results.Close()

	for results.Next() {
		party := &Party{}
		err := results.Scan(&party.Id, &party.OwnerUuid, &party.Name, &party.Public, &party.Pass, &party.SystemName, &party.Description)
		if err != nil {
			return parties, err
		}

		var hasOnlineMember bool

		for _, partyMember := range partyMembersByParty[party.Id] {
			party.Members = append(party.Members, *partyMember)
			if partyMember.Online {
				hasOnlineMember = true
			}
		}

		if hasOnlineMember {
			if simple {
				party.Pass = ""
			}
			parties = append(parties, party)
		}
	}

	return parties, nil
}

func readAllPartyMemberDataByParty(simple bool) (partyMembersByParty map[int][]*PartyMember, err error) {
	partyMembersByParty = make(map[int][]*PartyMember)

	results, err := db.Query("SELECT pm.partyId, pm.uuid, COALESCE(a.user, pgd.name), pd.rank, CASE WHEN a.user IS NULL THEN 0 ELSE 1 END, COALESCE(a.badge, ''), pgd.systemName, pgd.spriteName, pgd.spriteIndex FROM partyMembers pm JOIN playerGameData pgd ON pgd.uuid = pm.uuid JOIN players pd ON pd.uuid = pgd.uuid JOIN parties p ON p.id = pm.partyId LEFT JOIN accounts a ON a.uuid = pd.uuid WHERE pgd.game = ? ORDER BY CASE WHEN p.owner = pm.uuid THEN 0 ELSE 1 END, pd.rank DESC, pm.id", config.gameName)
	if err != nil {
		return partyMembersByParty, err
	}

	defer results.Close()

	var offlinePartyMembersByParty map[int][]*PartyMember = make(map[int][]*PartyMember)

	for results.Next() {
		var partyId int
		var accountBin int
		partyMember := &PartyMember{}
		err := results.Scan(&partyId, &partyMember.Uuid, &partyMember.Name, &partyMember.Rank, &accountBin, &partyMember.Badge, &partyMember.SystemName, &partyMember.SpriteName, &partyMember.SpriteIndex)
		if err != nil {
			return partyMembersByParty, err
		}
		partyMember.Account = accountBin == 1

		if client, ok := sessionClients[partyMember.Uuid]; ok {
			if client.name != "" {
				partyMember.Name = client.name
			}
			if client.systemName != "" {
				partyMember.SystemName = client.systemName
			}
			if client.spriteName != "" {
				partyMember.SpriteName = client.spriteName
			}
			if client.spriteIndex > -1 {
				partyMember.SpriteIndex = client.spriteIndex
			}
			if !simple {
				if hubClient, ok := hubClients[partyMember.Uuid]; ok {
					partyMember.MapId = hubClient.mapId
					partyMember.PrevMapId = hubClient.prevMapId
					partyMember.PrevLocations = hubClient.prevLocations
					partyMember.X = hubClient.x
					partyMember.Y = hubClient.y
				}
			}
			partyMember.Online = true

			partyMembersByParty[partyId] = append(partyMembersByParty[partyId], partyMember)
		} else {
			if !simple {
				partyMember.MapId = "0000"
				partyMember.PrevMapId = "0000"
			}
			offlinePartyMembersByParty[partyId] = append(offlinePartyMembersByParty[partyId], partyMember)
		}
	}

	for partyId, offlinePartyMembers := range offlinePartyMembersByParty {
		partyMembersByParty[partyId] = append(partyMembersByParty[partyId], offlinePartyMembers...)
	}

	return partyMembersByParty, nil
}

func readPartyData(playerUuid string) (party Party, err error) { //called by api only
	err = db.QueryRow("SELECT p.id, p.owner, p.name, p.public, p.pass, p.theme, p.description FROM parties p JOIN partyMembers pm ON pm.partyId = p.id JOIN playerGameData pgd ON pgd.uuid = pm.uuid AND pgd.game = p.game WHERE p.game = ? AND pm.uuid = ?", config.gameName, playerUuid).Scan(&party.Id, &party.OwnerUuid, &party.Name, &party.Public, &party.Pass, &party.SystemName, &party.Description)
	if err != nil {
		return party, err
	}

	partyMembers, err := readPartyMemberData(party.Id)
	if err != nil {
		return party, err
	}

	for _, partyMember := range partyMembers {
		party.Members = append(party.Members, *partyMember)
	}

	return party, nil
}

func readPartyMemberData(partyId int) (partyMembers []*PartyMember, err error) {
	results, err := db.Query("SELECT pm.partyId, pm.uuid, COALESCE(a.user, pgd.name), pd.rank, CASE WHEN a.user IS NULL THEN 0 ELSE 1 END, COALESCE(a.badge, ''), pgd.systemName, pgd.spriteName, pgd.spriteIndex FROM partyMembers pm JOIN playerGameData pgd ON pgd.uuid = pm.uuid JOIN players pd ON pd.uuid = pgd.uuid JOIN parties p ON p.id = pm.partyId LEFT JOIN accounts a ON a.uuid = pd.uuid WHERE pm.partyId = ? AND pgd.game = ? ORDER BY CASE WHEN p.owner = pm.uuid THEN 0 ELSE 1 END, pd.rank DESC, pm.id", partyId, config.gameName)
	if err != nil {
		return partyMembers, err
	}

	defer results.Close()

	for results.Next() {
		var partyId int
		var accountBin int
		partyMember := &PartyMember{}
		err := results.Scan(&partyId, &partyMember.Uuid, &partyMember.Name, &partyMember.Rank, &accountBin, &partyMember.Badge, &partyMember.SystemName, &partyMember.SpriteName, &partyMember.SpriteIndex)
		if err != nil {
			return partyMembers, err
		}
		partyMember.Account = accountBin == 1
		if client, ok := sessionClients[partyMember.Uuid]; ok {
			if client.name != "" {
				partyMember.Name = client.name
			}
			if client.systemName != "" {
				partyMember.SystemName = client.systemName
			}
			if client.spriteName != "" {
				partyMember.SpriteName = client.spriteName
			}
			if client.spriteIndex > -1 {
				partyMember.SpriteIndex = client.spriteIndex
			}
			if hubClient, ok := hubClients[partyMember.Uuid]; ok {
				partyMember.MapId = hubClient.mapId
				partyMember.PrevMapId = hubClient.prevMapId
				partyMember.PrevLocations = hubClient.prevLocations
				partyMember.X = hubClient.x
				partyMember.Y = hubClient.y
			}
			partyMember.Online = true
		}
		if partyMember.MapId == "" {
			partyMember.MapId = "0000"
		}
		if partyMember.PrevMapId == "" {
			partyMember.PrevMapId = "0000"
		}
		partyMembers = append(partyMembers, partyMember)
	}

	return partyMembers, nil
}

func readPartyDescription(partyId int) (description string, err error) { //called by api only
	err = db.QueryRow("SELECT description FROM parties WHERE id = ?", partyId).Scan(&description)
	if err != nil {
		return description, err
	}

	return description, nil
}

func readPartyPublic(partyId int) (public bool, err error) { //called by api only
	err = db.QueryRow("SELECT public FROM parties WHERE id = ?", partyId).Scan(&public)
	if err != nil {
		return public, err
	}

	return public, nil
}

func readPartyPass(partyId int) (pass string, err error) { //called by api only
	err = db.QueryRow("SELECT pass FROM parties WHERE id = ?", partyId).Scan(&pass)
	if err != nil {
		return pass, err
	}

	return pass, nil
}

func createPartyData(name string, public bool, pass string, theme string, description string, playerUuid string) (partyId int, err error) {
	res, err := db.Exec("INSERT INTO parties (game, owner, name, public, pass, theme, description) VALUES (?, ?, ?, ?, ?, ?, ?)", config.gameName, playerUuid, name, public, pass, theme, description)
	if err != nil {
		return 0, err
	}

	var partyId64 int64

	partyId64, err = res.LastInsertId()
	if err != nil {
		return 0, err
	}

	partyId = int(partyId64)

	return partyId, nil
}

func updatePartyData(partyId int, name string, public bool, pass string, theme string, description string, playerUuid string) (err error) {
	_, err = db.Exec("UPDATE parties SET game = ?, owner = ?, name = ?, public = ?, pass = ?, theme = ?, description = ? WHERE id = ?", config.gameName, playerUuid, name, public, pass, theme, description, partyId)
	if err != nil {
		return err
	}

	return nil
}

func createPlayerParty(partyId int, playerUuid string) error {
	_, err := db.Exec("INSERT INTO partyMembers (partyId, uuid) VALUES (?, ?)", partyId, playerUuid)
	if err != nil {
		return err
	}

	return nil
}

func clearPlayerParty(playerUuid string) error {
	_, err := db.Exec("DELETE pm FROM partyMembers pm JOIN parties p ON p.id = pm.partyId WHERE pm.uuid = ? AND p.game = ?", playerUuid, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func readPartyMemberUuids(partyId int) (partyMemberUuids []string, err error) {
	results, err := db.Query("SELECT pm.uuid FROM partyMembers pm JOIN players pd ON pd.uuid = pm.uuid WHERE pm.partyId = ? ORDER BY pd.rank DESC, pm.id", partyId)
	if err != nil {
		return partyMemberUuids, err
	}

	defer results.Close()

	for results.Next() {
		var uuid string
		err := results.Scan(&uuid)
		if err != nil {
			return partyMemberUuids, err
		}
		partyMemberUuids = append(partyMemberUuids, uuid)
	}

	return partyMemberUuids, nil
}

func readPartyMemberCount(partyId int) (count int, err error) {
	err = db.QueryRow("SELECT COUNT(*) FROM partyMembers WHERE partyId = ?", partyId).Scan(&count)
	if err != nil {
		return count, err
	}

	return count, nil
}

func readPartyOwnerUuid(partyId int) (ownerUuid string, err error) {
	err = db.QueryRow("SELECT owner FROM parties WHERE id = ?", partyId).Scan(&ownerUuid)
	if err != nil {
		return ownerUuid, err
	}

	return ownerUuid, nil
}

func assumeNextPartyOwner(partyId int) error {
	partyMemberUuids, err := readPartyMemberUuids(partyId)
	if err != nil {
		return err
	}

	var nextOnlinePlayerUuid string

	for _, uuid := range partyMemberUuids {
		if _, ok := hubClients[uuid]; ok {
			nextOnlinePlayerUuid = uuid
			break
		}
	}

	if nextOnlinePlayerUuid != "" {
		err = setPartyOwner(partyId, nextOnlinePlayerUuid)
		if err != nil {
			return err
		}
	} else {
		_, err := db.Exec("UPDATE parties p SET p.owner = (SELECT pm.uuid FROM partyMembers pm JOIN players pd ON pd.uuid = pm.uuid WHERE pm.partyId = p.id ORDER BY pd.rank DESC, pm.id LIMIT 1) WHERE p.id = ?", partyId)
		if err != nil {
			return err
		}
	}

	return nil
}

func setPartyOwner(partyId int, playerUuid string) error {
	_, err := db.Exec("UPDATE parties SET owner = ? WHERE id = ?", playerUuid, partyId)
	if err != nil {
		return err
	}

	return nil
}

func checkDeleteOrphanedParty(partyId int) (deleted bool, err error) {
	var partyMemberCount int
	partyMemberCount, err = readPartyMemberCount(partyId)
	if err != nil {
		return false, err
	}

	if partyMemberCount == 0 {
		_, err := db.Exec("DELETE FROM parties WHERE id = ?", partyId)
		if err != nil {
			return true, err
		}
		return true, nil
	}

	return false, nil
}

func deletePartyAndMembers(partyId int) (err error) {
	_, err = db.Exec("DELETE FROM partyMembers WHERE partyId = ?", partyId)
	if err != nil {
		return err
	}

	_, err = db.Exec("DELETE FROM parties WHERE id = ?", partyId)
	if err != nil {
		return err
	}

	return nil
}

func readSaveDataTimestamp(playerUuid string) (timestamp time.Time, err error) { //called by api only
	err = db.QueryRow("SELECT timestamp FROM gameSaves WHERE uuid = ? AND game = ?", playerUuid, config.gameName).Scan(&timestamp)
	if err != nil {
		return timestamp, err
	}

	return timestamp, nil
}

func readSaveData(playerUuid string) (saveData string, err error) { //called by api only
	err = db.QueryRow("SELECT data FROM gameSaves WHERE uuid = ? AND game = ?", playerUuid, config.gameName).Scan(&saveData)
	if err != nil {
		return saveData, err
	}

	return saveData, nil
}

func createGameSaveData(playerUuid string, timestamp time.Time, data string) (err error) { //called by api only
	_, err = db.Exec("INSERT INTO gameSaves (uuid, game, timestamp, data) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE timestamp = ?, data = ?", playerUuid, config.gameName, timestamp, data, timestamp, data)
	if err != nil {
		return err
	}

	return nil
}

func clearGameSaveData(playerUuid string) (err error) { //called by api only
	_, err = db.Exec("DELETE FROM gameSaves WHERE uuid = ? AND game = ?", playerUuid, config.gameName)
	if err != nil {
		return err
	}

	return nil
}

func readCurrentEventPeriodId() (periodId int, err error) {
	if currentEventPeriodId > -1 {
		return currentEventPeriodId, nil
	}

	err = db.QueryRow("SELECT id FROM eventPeriods WHERE game = ? AND UTC_DATE() >= startDate AND UTC_DATE() < endDate", config.gameName).Scan(&periodId)
	if err != nil {
		currentEventPeriodId = 0
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}

	currentEventPeriodId = periodId

	return periodId, nil
}

func readEventPeriodData() (eventPeriods []*EventPeriod, err error) {
	results, err := db.Query("SELECT periodOrdinal, endDate FROM eventPeriods WHERE game = ? AND periodOrdinal > 0", config.gameName)
	if err != nil {
		return eventPeriods, err
	}

	defer results.Close()

	for results.Next() {
		eventPeriod := &EventPeriod{}

		err := results.Scan(&eventPeriod.PeriodOrdinal, &eventPeriod.EndDate)
		if err != nil {
			return eventPeriods, err
		}

		eventPeriods = append(eventPeriods, eventPeriod)
	}

	return eventPeriods, nil
}

func readCurrentEventPeriodData() (eventPeriod EventPeriod, err error) {
	err = db.QueryRow("SELECT periodOrdinal, endDate FROM eventPeriods WHERE game = ? AND UTC_DATE() >= startDate AND UTC_DATE() < endDate", config.gameName).Scan(&eventPeriod.PeriodOrdinal, &eventPeriod.EndDate)
	if err != nil {
		eventPeriod.PeriodOrdinal = -1
		if err == sql.ErrNoRows {
			return eventPeriod, nil
		}
		return eventPeriod, err
	}

	return eventPeriod, nil
}

func readPlayerEventExpData(periodId int, playerUuid string) (eventExp EventExp, err error) {
	totalEventExp, err := readPlayerTotalEventExp(playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.TotalExp = totalEventExp

	periodEventExp, err := readPlayerPeriodEventExp(periodId, playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.PeriodExp = periodEventExp

	weekEventExp, err := readPlayerWeekEventExp(periodId, playerUuid)
	if err != nil {
		return eventExp, err
	}

	eventExp.WeekExp = weekEventExp

	return eventExp, nil
}

func readPlayerTotalEventExp(playerUuid string) (totalEventExp int, err error) {
	err = db.QueryRow("SELECT COALESCE(SUM(ec.exp), 0) FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN eventPeriods ep ON ep.id = el.periodId WHERE ep.game = ? AND ec.uuid = ?", config.gameName, playerUuid).Scan(&totalEventExp)
	if err != nil {
		return totalEventExp, err
	}

	return totalEventExp, nil
}

func readPlayerPeriodEventExp(periodId int, playerUuid string) (periodEventExp int, err error) {
	err = db.QueryRow("SELECT COALESCE(SUM(ec.exp), 0) FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN eventPeriods ep ON ep.id = el.periodId WHERE ep.id = ? AND ec.uuid = ?", periodId, playerUuid).Scan(&periodEventExp)
	if err != nil {
		return periodEventExp, err
	}

	return periodEventExp, nil
}

func readPlayerWeekEventExp(periodId int, playerUuid string) (weekEventExp int, err error) {
	weekdayIndex := int(time.Now().UTC().Weekday())

	err = db.QueryRow("SELECT COALESCE(SUM(ec.exp), 0) FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 JOIN eventPeriods ep ON ep.id = el.periodId WHERE ep.id = ? AND ec.uuid = ? AND DATE_SUB(UTC_DATE(), INTERVAL ? DAY) <= el.startDate AND DATE_ADD(UTC_DATE(), INTERVAL ? DAY) >= el.endDate", periodId, playerUuid, weekdayIndex, 7-weekdayIndex).Scan(&weekEventExp)
	if err != nil {
		return weekEventExp, err
	}

	return weekEventExp, nil
}

func readPlayerEventLocationCount(playerUuid string) (eventLocationCount int, err error) {
	err = db.QueryRow("SELECT COUNT(eventId) FROM eventCompletions WHERE uuid = ?", playerUuid).Scan(&eventLocationCount)
	if err != nil {
		return eventLocationCount, err
	}

	return eventLocationCount, nil
}

func readPlayerEventLocationCompletion(playerUuid string) (eventLocationCompletion int, err error) {
	err = db.QueryRow("SELECT COALESCE(FLOOR((COUNT(DISTINCT COALESCE(el.title, pel.title)) / aec.count) * 100), 0) FROM eventCompletions ec LEFT JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 LEFT JOIN playerEventLocations pel ON pel.id = ec.eventId AND ec.type = 1 JOIN (SELECT COUNT(DISTINCT COALESCE(ael.title, apel.title)) count FROM eventCompletions aec LEFT JOIN eventLocations ael ON ael.id = aec.eventId AND aec.type = 0 LEFT JOIN playerEventLocations	apel ON apel.id = aec.eventId AND aec.type = 1 WHERE (ael.title IS NOT NULL OR apel.title IS NOT NULL)) aec WHERE ec.uuid = ? AND (el.title IS NOT NULL OR pel.title IS NOT NULL)", playerUuid).Scan(&eventLocationCompletion)
	if err != nil {
		return eventLocationCompletion, err
	}

	return eventLocationCompletion, nil
}

func writeEventLocationData(periodId int, eventType int, title string, titleJP string, depth int, exp int, mapIds []string) (err error) {
	mapIdsJson, err := json.Marshal(mapIds)
	if err != nil {
		return err
	}

	var days int
	var offsetDays int
	weekday := time.Now().UTC().Weekday()
	if eventType == 0 {
		days = 1
	} else if eventType == 1 {
		days = 7
		offsetDays = int(weekday)
	} else {
		if weekday == time.Friday || weekday == time.Saturday {
			days = 2
			offsetDays = int(weekday) - int(time.Friday)
		} else {
			return nil
		}
	}

	days -= offsetDays

	_, err = db.Exec("INSERT INTO eventLocations (periodId, type, title, titleJP, depth, exp, startDate, endDate, mapIds) VALUES (?, ?, ?, ?, ?, ?, DATE_SUB(UTC_DATE(), INTERVAL ? DAY), DATE_ADD(UTC_DATE(), INTERVAL ? DAY), ?)", periodId, eventType, title, titleJP, depth, exp, offsetDays, days, mapIdsJson)
	if err != nil {
		return err
	}

	return nil
}

func writePlayerEventLocationData(periodId int, playerUuid string, title string, titleJP string, depth int, mapIds []string) (err error) {
	mapIdsJson, err := json.Marshal(mapIds)
	if err != nil {
		return err
	}

	_, err = db.Exec("INSERT INTO playerEventLocations (periodId, uuid, title, titleJP, depth, startDate, endDate, mapIds) VALUES (?, ?, ?, ?, ?, UTC_DATE(), DATE_ADD(UTC_DATE(), INTERVAL 1 DAY), ?)", periodId, playerUuid, title, titleJP, depth, mapIdsJson)
	if err != nil {
		return err
	}

	return nil
}

func readCurrentPlayerEventLocationsData(periodId int, playerUuid string) (eventLocations []*EventLocation, err error) {
	results, err := db.Query("SELECT el.id, el.type, el.title, el.titleJP, el.depth, el.exp, el.endDate, CASE WHEN ec.uuid IS NOT NULL THEN 1 ELSE 0 END FROM eventLocations el LEFT JOIN eventCompletions ec ON ec.eventId = el.id AND ec.type = 0 AND ec.uuid = ? WHERE el.periodId = ? AND UTC_DATE() >= el.startDate AND UTC_DATE() < el.endDate ORDER BY 2, 1", playerUuid, periodId)
	if err != nil {
		return eventLocations, err
	}

	for results.Next() {
		eventLocation := &EventLocation{}

		var completeBin int

		err := results.Scan(&eventLocation.Id, &eventLocation.Type, &eventLocation.Title, &eventLocation.TitleJP, &eventLocation.Depth, &eventLocation.Exp, &eventLocation.EndDate, &completeBin)
		if err != nil {
			return eventLocations, err
		}

		if completeBin == 1 {
			eventLocation.Complete = true
		}

		eventLocations = append(eventLocations, eventLocation)
	}

	results.Close()

	results, err = db.Query("SELECT pel.id, pel.title, pel.titleJP, pel.depth, pel.endDate FROM playerEventLocations pel LEFT JOIN eventCompletions ec ON ec.eventId = pel.id AND ec.type = 1 AND ec.uuid = pel.uuid WHERE pel.uuid = ? AND pel.periodId = ? AND ec.uuid IS NULL AND UTC_DATE() >= pel.startDate AND UTC_DATE() < pel.endDate ORDER BY 1", playerUuid, periodId)
	if err != nil {
		return eventLocations, err
	}

	for results.Next() {
		eventLocation := &EventLocation{}

		err := results.Scan(&eventLocation.Id, &eventLocation.Title, &eventLocation.TitleJP, &eventLocation.Depth, &eventLocation.EndDate)
		if err != nil {
			return eventLocations, err
		}

		eventLocation.Type = -1

		eventLocations = append(eventLocations, eventLocation)
	}

	results.Close()

	return eventLocations, nil
}

func tryCompleteEventLocation(periodId int, playerUuid string, location string) (exp int, err error) {
	if client, ok := hubClients[playerUuid]; ok {
		clientMapId := client.mapId

		results, err := db.Query("SELECT el.id, el.type, el.exp, el.mapIds FROM eventLocations el WHERE el.periodId = ? AND el.title = ? AND UTC_DATE() >= el.startDate AND UTC_DATE() < el.endDate ORDER BY 2", periodId, location)
		if err != nil {
			return -1, err
		}

		defer results.Close()

		weekEventExp, err := readPlayerWeekEventExp(periodId, playerUuid)
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

				for updatingRankings {
					time.Sleep(100 * time.Millisecond) //wait until rankings are updated
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

func tryCompletePlayerEventLocation(periodId int, playerUuid string, location string) (complete bool, err error) {
	if client, ok := hubClients[playerUuid]; ok {
		clientMapId := client.mapId

		results, err := db.Query("SELECT pel.id, pel.mapIds FROM playerEventLocations pel WHERE pel.periodId = ? AND pel.title = ? AND pel.uuid = ? AND UTC_DATE() >= pel.startDate AND UTC_DATE() < pel.endDate ORDER BY 2", periodId, location, playerUuid)
		if err != nil {
			return false, err
		}

		defer results.Close()

		var success bool

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

func readCurrentPlayerEventVmsData(periodId int, playerUuid string) (eventVms []*EventVm, err error) {
	results, err := db.Query("SELECT ev.id, ev.exp, ev.endDate, CASE WHEN ec.uuid IS NOT NULL THEN 1 ELSE 0 END FROM eventVms ev LEFT JOIN eventCompletions ec ON ec.eventId = ev.id AND ec.type = 2 AND ec.uuid = ? WHERE ev.periodId = ? AND UTC_DATE() >= ev.startDate AND UTC_DATE() < ev.endDate ORDER BY 2, 1", playerUuid, periodId)
	if err != nil {
		return eventVms, err
	}

	for results.Next() {
		eventVm := &EventVm{}

		var completeBin int

		err := results.Scan(&eventVm.Id, &eventVm.Exp, &eventVm.EndDate, &completeBin)
		if err != nil {
			return eventVms, err
		}

		if completeBin == 1 {
			eventVm.Complete = true
		}

		eventVms = append(eventVms, eventVm)
	}

	results.Close()

	return eventVms, nil
}

func readEventVmInfo(id int) (mapId int, eventId int, err error) {
	err = db.QueryRow("SELECT mapId, eventId FROM eventVms WHERE id = ?", id).Scan(&mapId, &eventId)
	if err != nil {
		return 0, 0, err
	}

	return mapId, eventId, nil
}

func writeEventVmData(periodId int, mapId int, eventId int, exp int) (err error) {
	var days int
	var offsetDays int
	weekday := time.Now().UTC().Weekday()

	switch weekday {
	case time.Sunday:
		fallthrough
	case time.Monday:
		days = 2
		offsetDays = int(weekday)
	case time.Tuesday:
		fallthrough
	case time.Wednesday:
		fallthrough
	case time.Thursday:
		days = 3
		offsetDays = int(weekday) - int(time.Tuesday)
	case time.Friday:
		fallthrough
	case time.Saturday:
		days = 2
		offsetDays = int(weekday) - int(time.Friday)
	}

	days -= offsetDays

	_, err = db.Exec("INSERT INTO eventVms (periodId, mapId, eventId, exp, startDate, endDate) VALUES (?, ?, ?, ?, DATE_SUB(UTC_DATE(), INTERVAL ? DAY), DATE_ADD(UTC_DATE(), INTERVAL ? DAY))", periodId, mapId, eventId, exp, offsetDays, days)
	if err != nil {
		return err
	}

	return nil
}

func tryCompleteEventVm(periodId int, playerUuid string, mapId int, eventId int) (exp int, err error) {
	if client, ok := hubClients[playerUuid]; ok {
		clientMapId := client.mapId

		results, err := db.Query("SELECT ev.id, ev.mapId, ev.eventId, ev.exp FROM eventVms ev WHERE ev.periodId = ? AND ev.mapId = ? AND ev.eventId = ? AND UTC_DATE() >= ev.startDate AND UTC_DATE() < ev.endDate ORDER BY 2", periodId, mapId, eventId)
		if err != nil {
			return -1, err
		}

		defer results.Close()

		weekEventExp, err := readPlayerWeekEventExp(periodId, playerUuid)
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

			if clientMapId != fmt.Sprintf("%04d", eventMapId) {
				continue
			}
			if weekEventExp >= weeklyExpCap {
				eventExp = 0
			} else if weekEventExp+eventExp > weeklyExpCap {
				eventExp = weeklyExpCap - weekEventExp
			}

			for updatingRankings {
				time.Sleep(100 * time.Millisecond) //wait until rankings are updated
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

func writeGameBadges() (err error) {
	_, err = db.Exec("TRUNCATE TABLE badges")
	if err != nil {
		return err
	}

	for badgeGame := range badges {
		for badgeId, badge := range badges[badgeGame] {
			_, err = db.Exec("INSERT INTO badges (badgeId, game, bp, hidden) VALUES (?, ?, ?, ?)", badgeId, badgeGame, badge.Bp, badge.Hidden || badge.Dev)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func readPlayerUnlockedBadgeIds(playerUuid string) (unlockedBadgeIds []string, err error) {
	results, err := db.Query("SELECT badgeId FROM playerBadges WHERE uuid = ?", playerUuid)
	if err != nil {
		return unlockedBadgeIds, err
	}

	defer results.Close()

	for results.Next() {
		var badgeId string
		err := results.Scan(&badgeId)
		if err != nil {
			return unlockedBadgeIds, err
		}
		unlockedBadgeIds = append(unlockedBadgeIds, badgeId)
	}

	return unlockedBadgeIds, nil
}

func unlockPlayerBadge(playerUuid string, badgeId string) (err error) {
	_, err = db.Exec("INSERT INTO playerBadges (uuid, badgeId, timestampUnlocked) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE badgeId = badgeId", playerUuid, badgeId, time.Now())
	if err != nil {
		return err
	}

	return nil
}

func removePlayerBadge(playerUuid string, badgeId string) (err error) {
	var slotRow int
	var slotCol int

	err = db.QueryRow("SELECT slotRow, slotCol FROM playerBadges WHERE uuid = ? AND badgeId = ?", playerUuid, badgeId).Scan(&slotRow, &slotCol)
	if err != nil {
		return err
	}

	_, err = db.Exec("DELETE FROM playerBadges WHERE uuid = ? AND badgeId = ?", playerUuid, badgeId)
	if err != nil {
		return err
	}

	_, err = db.Exec("UPDATE accounts SET badge = 'null' WHERE uuid = ? AND badge = ?", playerUuid, badgeId)
	if err != nil {
		return err
	}

	return nil
}

func readBadgeUnlockPercentages() (unlockPercentages []*BadgePercentUnlocked, err error) {
	results, err := db.Query("SELECT b.badgeId, (COUNT(b.uuid) / aa.count) * 100 FROM playerBadges b JOIN accounts a ON a.uuid = b.uuid JOIN (SELECT COUNT(aa.uuid) count FROM accounts aa WHERE aa.timestampLoggedIn IS NOT NULL) aa WHERE a.timestampLoggedIn IS NOT NULL GROUP BY b.badgeId")
	if err != nil {
		return unlockPercentages, err
	}

	defer results.Close()

	for results.Next() {
		percentUnlocked := &BadgePercentUnlocked{}

		err := results.Scan(&percentUnlocked.BadgeId, &percentUnlocked.Percent)
		if err != nil {
			return unlockPercentages, err
		}

		unlockPercentages = append(unlockPercentages, percentUnlocked)
	}

	return unlockPercentages, nil
}

func readPlayerTags(playerUuid string) (tags []string, err error) {
	results, err := db.Query("SELECT name FROM playerTags WHERE uuid = ?", playerUuid)
	if err != nil {
		return tags, err
	}

	defer results.Close()

	for results.Next() {
		var tagName string
		err := results.Scan(&tagName)
		if err != nil {
			return tags, err
		}
		tags = append(tags, tagName)
	}

	return tags, nil
}

func tryWritePlayerTag(playerUuid string, name string) (success bool, err error) {
	if client, ok := hubClients[playerUuid]; ok { // Player must be online to add a tag
		// Spare SQL having to deal with a duplicate record by checking player tags beforehand
		tags := client.tags
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

func readTimeTrialMapIds() (mapIds []int, err error) {
	results, err := db.Query("SELECT mapId FROM playerTimeTrials GROUP BY mapId ORDER BY MIN(seconds)")
	if err != nil {
		return mapIds, err
	}

	defer results.Close()

	for results.Next() {
		var mapId int
		err := results.Scan(&mapId)
		if err != nil {
			return mapIds, err
		}

		mapIds = append(mapIds, mapId)
	}

	return mapIds, nil
}

func readPlayerTimeTrialRecords(playerUuid string) (timeTrialRecords []*TimeTrialRecord, err error) {
	results, err := db.Query("SELECT mapId, MIN(seconds) FROM playerTimeTrials WHERE uuid = ? GROUP BY mapId", playerUuid)
	if err != nil {
		return timeTrialRecords, err
	}

	defer results.Close()

	for results.Next() {
		timeTrialRecord := &TimeTrialRecord{}

		err := results.Scan(&timeTrialRecord.MapId, &timeTrialRecord.Seconds)
		if err != nil {
			return timeTrialRecords, err
		}

		timeTrialRecords = append(timeTrialRecords, timeTrialRecord)
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

func readGameMinigameIds() (minigameIds []string, err error) {
	results, err := db.Query("SELECT DISTINCT minigameId FROM playerMinigameScores WHERE game = ? ORDER BY minigameId", config.gameName)
	if err != nil {
		return minigameIds, err
	}

	defer results.Close()

	for results.Next() {
		var minigameId string
		err := results.Scan(&minigameId)
		if err != nil {
			return minigameIds, err
		}

		minigameIds = append(minigameIds, minigameId)
	}

	return minigameIds, nil
}

func readPlayerMinigameScore(playerUuid string, minigameId string) (score int, err error) {
	err = db.QueryRow("SELECT score FROM playerMinigameScores WHERE uuid = ? AND minigameId = ?", playerUuid, minigameId).Scan(&score)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}

	return score, nil
}

func tryWritePlayerMinigameScore(playerUuid string, minigameId string, score int) (success bool, err error) {
	if score <= 0 {
		return false, nil
	}

	prevScore, err := readPlayerMinigameScore(playerUuid, minigameId)
	if err != nil {
		return false, err
	} else if score <= prevScore {
		return false, nil
	} else if prevScore > 0 {
		_, err = db.Exec("UPDATE playerMinigameScores SET score = ?, timestampCompleted = ? WHERE uuid = ? AND game = ? AND minigameId = ?", score, time.Now(), playerUuid, config.gameName, minigameId)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	_, err = db.Exec("INSERT INTO playerMinigameScores (uuid, game, minigameId, score, timestampCompleted) VALUES (?, ?, ?, ?, ?)", playerUuid, config.gameName, minigameId, score, time.Now())
	if err != nil {
		return false, err
	}

	return true, nil
}

func readRankingCategories() (rankingCategories []*RankingCategory, err error) {
	results, err := db.Query("SELECT categoryId, game FROM rankingCategories WHERE game IN ('', ?) ORDER BY ordinal", config.gameName)
	if err != nil {
		return rankingCategories, err
	}

	for results.Next() {
		rankingCategory := &RankingCategory{}

		err := results.Scan(&rankingCategory.CategoryId, &rankingCategory.Game)
		if err != nil {
			return rankingCategories, err
		}

		rankingCategories = append(rankingCategories, rankingCategory)
	}

	results.Close()

	results, err = db.Query("SELECT sc.categoryId, sc.subCategoryId, sc.game, CEILING(COUNT(r.uuid) / 25) FROM rankingSubCategories sc JOIN rankingEntries r ON r.categoryId = sc.categoryId AND r.subCategoryId = sc.subCategoryId WHERE sc.game IN ('', ?) GROUP BY sc.categoryId, sc.subCategoryId, sc.game ORDER BY 1, sc.ordinal", config.gameName)
	if err != nil {
		return rankingCategories, err
	}

	var lastCategoryId string
	var lastCategory *RankingCategory

	for results.Next() {
		rankingSubCategory := &RankingSubCategory{}

		var categoryId string
		err := results.Scan(&categoryId, &rankingSubCategory.SubCategoryId, &rankingSubCategory.Game, &rankingSubCategory.PageCount)
		if err != nil {
			return rankingCategories, err
		}

		if lastCategoryId != categoryId {
			lastCategoryId = categoryId
			for _, rankingCategory := range rankingCategories {
				if rankingCategory.CategoryId == lastCategoryId {
					lastCategory = rankingCategory
				}
			}
		}

		lastCategory.SubCategories = append(lastCategory.SubCategories, *rankingSubCategory)
	}

	results.Close()

	return rankingCategories, nil
}

func writeRankingCategory(categoryId string, game string, order int) (err error) {
	_, err = db.Exec("INSERT INTO rankingCategories (categoryId, game, ordinal) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE ordinal = ?", categoryId, game, order, order)
	if err != nil {
		return err
	}

	return nil
}

func writeRankingSubCategory(categoryId string, subCategoryId string, game string, order int) (err error) {
	_, err = db.Exec("INSERT INTO rankingSubCategories (categoryId, subCategoryId, game, ordinal) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE ordinal = ?", categoryId, subCategoryId, game, order, order)
	if err != nil {
		return err
	}

	return nil
}

func readRankingEntryPage(playerUuid string, categoryId string, subCategoryId string) (page int, err error) {
	err = db.QueryRow("SELECT FLOOR(r.rowNum / 25) + 1 FROM (SELECT r.uuid, ROW_NUMBER() OVER (ORDER BY r.position) rowNum FROM rankingEntries r WHERE r.categoryId = ? AND r.subCategoryId = ?) r WHERE r.uuid = ?", categoryId, subCategoryId, playerUuid).Scan(&page)
	if err != nil {
		if err == sql.ErrNoRows {
			return 1, nil
		}
		return 1, err
	}

	return page, nil
}

func readRankingsPaged(categoryId string, subCategoryId string, page int) (rankings []*Ranking, err error) {
	var valueType string
	switch categoryId {
	case "eventLocationCompletion":
		valueType = "Float"
	default:
		valueType = "Int"
	}

	results, err := db.Query("SELECT r.position, a.user, pd.rank, a.badge, COALESCE(pgd.systemName, ''), r.value"+valueType+" FROM rankingEntries r JOIN accounts a ON a.uuid = r.uuid JOIN players pd ON pd.uuid = a.uuid LEFT JOIN playerGameData pgd ON pgd.uuid = pd.uuid AND pgd.game = ? WHERE r.categoryId = ? AND r.subCategoryId = ? ORDER BY 1, r.timestamp LIMIT "+strconv.Itoa((page-1)*25)+", 25", config.gameName, categoryId, subCategoryId)
	if err != nil {
		return rankings, err
	}

	defer results.Close()

	for results.Next() {
		ranking := &Ranking{}

		if valueType == "Int" {
			err = results.Scan(&ranking.Position, &ranking.Name, &ranking.Rank, &ranking.Badge, &ranking.SystemName, &ranking.ValueInt)
		} else {
			err = results.Scan(&ranking.Position, &ranking.Name, &ranking.Rank, &ranking.Badge, &ranking.SystemName, &ranking.ValueFloat)
		}
		if err != nil {
			return rankings, err
		}

		rankings = append(rankings, ranking)
	}

	return rankings, nil
}

func updateRankingEntries(categoryId string, subCategoryId string) (err error) {
	var valueType string
	switch categoryId {
	case "eventLocationCompletion":
		valueType = "Float"
	default:
		valueType = "Int"
	}

	_, err = db.Exec("DELETE FROM rankingEntries WHERE categoryId = ? AND subCategoryId = ?", categoryId, subCategoryId)
	if err != nil {
		return err
	}

	isFiltered := subCategoryId != "all"

	query := "INSERT INTO rankingEntries (categoryId, subCategoryId, position, uuid, value" + valueType + ", timestamp) "

	switch categoryId {
	case "badgeCount":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY COUNT(pb.uuid) DESC), a.uuid, COUNT(pb.uuid), (SELECT MAX(apb.timestampUnlocked) FROM playerBadges apb WHERE apb.uuid = a.uuid AND apb.badgeId = b.badgeId) FROM playerBadges pb JOIN accounts a ON a.uuid = pb.uuid JOIN badges b ON b.badgeId = pb.badgeId WHERE b.hidden = 0"
		if isFiltered {
			query += " AND b.game = ?"
		}
		query += " GROUP BY a.uuid ORDER BY 5 DESC, 6"
	case "bp":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY SUM(b.bp) DESC), a.uuid, SUM(b.bp), (SELECT MAX(apb.timestampUnlocked) FROM playerBadges apb WHERE apb.uuid = a.uuid AND apb.badgeId = b.badgeId) FROM playerBadges pb JOIN accounts a ON a.uuid = pb.uuid JOIN badges b ON b.badgeId = pb.badgeId"
		if isFiltered {
			query += " WHERE b.game = ?"
		}
		query += " GROUP BY a.uuid ORDER BY 5 DESC, 6"
	case "exp":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY SUM(ec.exp) DESC), ec.uuid, SUM(ec.exp), (SELECT MAX(aec.timestampCompleted) FROM eventCompletions aec WHERE aec.uuid = ec.uuid) FROM eventCompletions ec JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0"
		if isFiltered {
			query += " JOIN eventPeriods ep ON ep.id = el.periodId AND ep.periodOrdinal = ?"
		}
		query += " GROUP BY ec.uuid ORDER BY 5 DESC, 6"
	case "eventLocationCount":
		fallthrough
	case "freeEventLocationCount":
		isFree := categoryId == "freeEventLocationCount"
		query += "SELECT ?, ?, RANK() OVER (ORDER BY COUNT(ec.uuid) DESC), ec.uuid, COUNT(ec.uuid), (SELECT MAX(aec.timestampCompleted) FROM eventCompletions aec WHERE aec.uuid = ec.uuid) FROM eventCompletions ec "
		if isFiltered {
			if isFree {
				query += "JOIN playerEventLocations el"
			} else {
				query += "JOIN eventLocations el"
			}
			query += " ON el.id = ec.eventId JOIN eventPeriods ep ON ep.id = el.periodId AND ep.periodOrdinal = ? "
		}
		query += "WHERE ec.type = "
		if isFree {
			query += "1"
		} else {
			query += "0"
		}
		query += " GROUP BY ec.uuid ORDER BY 5 DESC, 6"
	case "eventLocationCompletion":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY COUNT(DISTINCT COALESCE(el.title, pel.title)) / aec.count DESC), a.uuid, COUNT(DISTINCT COALESCE(el.title, pel.title)) / aec.count, (SELECT MAX(aect.timestampCompleted) FROM eventCompletions aect WHERE aect.uuid = ec.uuid) FROM eventCompletions ec JOIN accounts a ON a.uuid = ec.uuid LEFT JOIN eventLocations el ON el.id = ec.eventId AND ec.type = 0 LEFT JOIN playerEventLocations pel ON pel.id = ec.eventId AND ec.type = 1 JOIN (SELECT COUNT(DISTINCT COALESCE(ael.title, apel.title)) count FROM eventCompletions aec LEFT JOIN eventLocations ael ON ael.id = aec.eventId AND aec.type = 0 LEFT JOIN playerEventLocations apel ON apel.id = aec.eventId AND aec.type = 1 WHERE (ael.title IS NOT NULL OR apel.title IS NOT NULL)) aec"
		if isFiltered {
			query += " JOIN eventPeriods ep ON ep.id = COALESCE(el.periodId, pel.periodId) AND ep.periodOrdinal = ?"
		}
		query += " GROUP BY a.user ORDER BY 5 DESC, 6"
	case "timeTrial":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY MIN(tt.seconds)), tt.uuid, MIN(tt.seconds), (SELECT MAX(att.timestampCompleted) FROM playerTimeTrials att WHERE att.uuid = tt.uuid AND att.mapId = tt.mapId AND att.seconds = tt.seconds) FROM playerTimeTrials tt WHERE tt.mapId = ? GROUP BY tt.uuid ORDER BY 5, 6"
	case "minigame":
		query += "SELECT ?, ?, RANK() OVER (ORDER BY MAX(ms.score) DESC), ms.uuid, MAX(ms.score), (SELECT MAX(ams.timestampCompleted) FROM playerMinigameScores ams WHERE ams.uuid = ms.uuid AND ams.minigameId = ms.minigameId AND ams.score = ms.score) FROM playerMinigameScores ms WHERE ms.minigameId = ? GROUP BY ms.uuid ORDER BY 5 DESC, 6"
	}

	if isFiltered {
		_, err = db.Exec(query, categoryId, subCategoryId, subCategoryId)
	} else {
		_, err = db.Exec(query, categoryId, subCategoryId)
	}

	if err != nil {
		return err
	}

	return nil
}
