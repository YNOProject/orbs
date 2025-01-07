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
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type PlayerInfo struct {
	Uuid            string `json:"uuid"`
	Name            string `json:"name"`
	Rank            int    `json:"rank"`
	Badge           string `json:"badge"`
	BadgeSlotRows   int    `json:"badgeSlotRows"`
	BadgeSlotCols   int    `json:"badgeSlotCols"`
	ScreenshotLimit int    `json:"screenshotLimit"`
	Medals          [5]int `json:"medals"`
	LocationIds     []int  `json:"locationIds"`
}

type PlayerListData struct {
	Uuid       string `json:"uuid"`
	Name       string `json:"name"`
	SystemName string `json:"systemName"`
	Rank       int    `json:"rank"`
	Account    bool   `json:"account"`
	Badge      string `json:"badge"`
	Medals     [5]int `json:"medals"`

	SpriteName  string `json:"spriteName"`
	SpriteIndex int    `json:"spriteIndex"`
}

type PlayerListFullData struct {
	PlayerListData

	MapId         string `json:"mapId,omitempty"`
	PrevMapId     string `json:"prevMapId,omitempty"`
	PrevLocations string `json:"prevLocations,omitempty"`
	X             int    `json:"x"`
	Y             int    `json:"y"`

	Online     bool      `json:"online"`
	LastActive time.Time `json:"lastActive"`
}

const (
	actionBan = iota
	actionMute
)

type CheckUpdateData struct {
	BadgeIds []string `json:"badgeIds"`
	NewTags  bool     `json:"newTags"`
}

func initApi() {
	logInitTask("API")

	http.HandleFunc("/session", handleSession)
	http.HandleFunc("/room", handleRoom)

	http.HandleFunc("/admin/getplayers", adminGetPlayers)
	http.HandleFunc("/admin/getbans", adminGetBansMutes)
	http.HandleFunc("/admin/getmutes", adminGetBansMutes)
	http.HandleFunc("/admin/ban", adminBanMute)
	http.HandleFunc("/admin/mute", adminBanMute)
	http.HandleFunc("/admin/unban", adminBanMute)
	http.HandleFunc("/admin/unmute", adminBanMute)
	http.HandleFunc("/admin/changeusername", adminChangeUsername)
	http.HandleFunc("/admin/resetpw", adminResetPw)
	http.HandleFunc("/admin/grantbadge", adminManageBadge)
	http.HandleFunc("/admin/revokebadge", adminManageBadge)

	http.HandleFunc("/api/party", handleParty)
	http.HandleFunc("/api/savesync", handleSaveSync)
	http.HandleFunc("/api/vm", handleVm)
	http.HandleFunc("/api/badge", handleBadge)

	http.HandleFunc("/api/register", handleRegister)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/logout", handleLogout)
	http.HandleFunc("/api/changepw", handleChangePw)

	http.HandleFunc("/api/addplayerfriend", handleAddPlayerFriend)
	http.HandleFunc("/api/removeplayerfriend", handleRemovePlayerFriend)

	http.HandleFunc("/api/blockplayer", handleBlockPlayer)
	http.HandleFunc("/api/unblockplayer", handleUnblockPlayer)
	http.HandleFunc("/api/blocklist", handleBlockList)

	http.HandleFunc("/api/chathistory", handleChatHistory)
	http.HandleFunc("/api/clearchathistory", handleClearChatHistory)

	http.HandleFunc("/api/gamelocations", handleGameLocations)

	http.HandleFunc("/api/screenshot", handleScreenshot)

	http.HandleFunc("/api/2kki", handle2kki)

	http.HandleFunc("/api/explorer", handleExplorer)
	http.HandleFunc("/api/explorercompletion", handleExplorerCompletion)
	http.HandleFunc("/api/explorerlocations", handleExplorerLocations)

	http.HandleFunc("/api/info", handleInfo)

	http.HandleFunc("/api/players", handlePlayers)

	http.HandleFunc("/api/schedule", handleSchedules)
	http.HandleFunc("/api/registernotification", handleRegisterSubscriber)
	http.HandleFunc("/api/unregisternotification", handleUnregisterSubscriber)
	http.HandleFunc("/api/vapidpublickey", handleVapidPublicKeyRequest)

	http.HandleFunc("/api/report", handleReport)
}

func handleParty(w http.ResponseWriter, r *http.Request) {
	var uuid string
	var rank int
	var banned bool

	token := r.Header.Get("Authorization")
	if token == "" {
		uuid, banned, _ = getOrCreatePlayerData(getIp(r))
	} else {
		uuid, _, rank, _, banned, _ = getPlayerDataFromToken(token)
		if uuid == "" {
			handleError(w, r, "invalid token")
			return
		}
	}

	if banned {
		handleError(w, r, "player is banned")
		return
	}

	commandParam := r.URL.Query().Get("command")
	if commandParam == "" {
		handleError(w, r, "command not specified")
		return
	}

	switch commandParam {
	case "id":
		partyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		w.Write([]byte(strconv.Itoa(partyId)))
		return
	case "list":
		partyListData, err := getAllPartyData()
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		partyListDataJson, err := json.Marshal(partyListData)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		w.Write(partyListDataJson)
		return
	case "description":
		partyIdParam := r.URL.Query().Get("partyId")
		if partyIdParam == "" {
			handleError(w, r, "partyId not specified")
			return
		}
		partyId, err := strconv.Atoi(partyIdParam)
		if err != nil {
			handleError(w, r, "invalid partyId value")
			return
		}
		party, ok := parties[partyId]
		if !ok {
			handleInternalError(w, r, errors.New("party id not in cache"))
		}
		w.Write([]byte(party.Description))
		return
	case "create", "update":
		partyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		create := commandParam == "create"
		if create {
			if partyId != 0 {
				err = handlePartyMemberLeave(partyId, uuid)
				if err != nil {
					handleInternalError(w, r, err)
					return
				}
			}
		} else {
			if partyId == 0 {
				handleError(w, r, "player not in a party")
				return
			}
			ownerUuid, err := getPartyOwnerUuid(partyId)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			if ownerUuid != uuid {
				handleError(w, r, "attempted party update from non-owner")
				return
			}
		}
		nameParam := r.URL.Query().Get("name")
		if nameParam == "" {
			handleError(w, r, "name not specified")
			return
		}
		if len(nameParam) > 255 {
			handleError(w, r, "name too long")
			return
		}
		var description string
		descriptionParam := r.URL.Query().Get("description")
		if descriptionParam != "" {
			description = descriptionParam
		}
		var public bool
		publicParam := r.URL.Query().Get("public")
		if publicParam != "" {
			public = true
		}
		var pass string
		if !public {
			passParam := r.URL.Query().Get("pass")
			if passParam != "" {
				if len(passParam) > 255 {
					handleError(w, r, "pass too long")
					return
				}
				pass = passParam
			}
		}
		themeParam := r.URL.Query().Get("theme")
		if themeParam == "" {
			handleError(w, r, "theme not specified")
			return
		}
		if !assets.IsValidSystem(themeParam, true) {
			handleError(w, r, "invalid system name for theme")
			return
		}
		if create {
			partyId, err = createPartyData(nameParam, public, pass, themeParam, description, uuid)
		} else {
			err = updatePartyData(partyId, nameParam, public, pass, themeParam, description, uuid)
		}
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if create {
			err = joinPlayerParty(partyId, uuid)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			w.Write([]byte(strconv.Itoa(partyId)))
			return
		}
	case "join":
		partyIdParam := r.URL.Query().Get("partyId")
		if partyIdParam == "" {
			handleError(w, r, "partyId not specified")
			return
		}
		partyId, err := strconv.Atoi(partyIdParam)
		if err != nil {
			handleError(w, r, "invalid partyId value")
			return
		}
		if rank == 0 {
			party, ok := parties[partyId]
			if !ok {
				handleInternalError(w, r, errors.New("party id not in cache"))
				return
			}
			if !party.Public {
				passParam := r.URL.Query().Get("pass")
				if passParam == "" {
					handleError(w, r, "pass not specified")
					return
				}
				if party.Pass != "" && passParam != party.Pass {
					http.Error(w, "401 - Unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}
		playerPartyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if playerPartyId != 0 {
			err = handlePartyMemberLeave(partyId, uuid)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
		}
		err = joinPlayerParty(partyId, uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
	case "leave":
		partyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if partyId == 0 {
			handleError(w, r, "player not in a party")
			return
		}
		err = handlePartyMemberLeave(partyId, uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
	case "kick", "transfer":
		kick := commandParam == "kick"
		partyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if partyId == 0 {
			handleError(w, r, "player not in a party")
			return
		}
		ownerUuid, err := getPartyOwnerUuid(partyId)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if ownerUuid != uuid {
			if kick {
				handleError(w, r, "attempted party kick non-owner")
			} else {
				handleError(w, r, "attempted owner transfer from non-owner")
			}
			return
		}
		playerParam := r.URL.Query().Get("player")
		if playerParam == "" {
			handleError(w, r, "player not specified")
			return
		}
		playerPartyId, err := getPlayerPartyId(playerParam)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if playerPartyId != partyId {
			if kick {
				handleError(w, r, "specified player to kick not in same party")
			} else {
				handleError(w, r, "specified player to transfer owner not in same party")
			}
			return
		}
		if kick {
			err = leavePlayerParty(playerParam)
		} else {
			err = setPartyOwner(partyId, playerParam)
		}
		if err != nil {
			handleInternalError(w, r, nil)
		}
	case "disband":
		partyId, err := getPlayerPartyId(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		ownerUuid, err := getPartyOwnerUuid(partyId)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if ownerUuid != uuid {
			handleError(w, r, "attempted party disband from non-owner")
			return
		}
		err = deletePartyAndMembers(partyId)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
	default:
		handleError(w, r, "unknown command")
		return
	}

	w.Write([]byte("ok"))
}

func handlePartyMemberLeave(partyId int, playerUuid string) error {
	ownerUuid, err := getPartyOwnerUuid(partyId)
	if err != nil {
		return err
	}

	err = leavePlayerParty(playerUuid)
	if err != nil {
		return err
	}

	deleted, err := checkDeleteOrphanedParty(partyId)
	if err != nil {
		return err
	}
	if !deleted && playerUuid == ownerUuid {
		err = assumeNextPartyOwner(partyId)
		if err != nil {
			return err
		}
	}

	return nil
}

func handleSaveSync(w http.ResponseWriter, r *http.Request) {
	var uuid string
	var banned bool

	token := r.Header.Get("Authorization")
	if token == "" {
		handleError(w, r, "token not specified")
		return
	} else {
		uuid, _, _, _, banned, _ = getPlayerDataFromToken(token)
		if uuid == "" {
			handleError(w, r, "invalid token")
			return
		}
	}

	if banned {
		handleError(w, r, "player is banned")
		return
	}

	commandParam := r.URL.Query().Get("command")
	if commandParam == "" {
		handleError(w, r, "command not specified")
		return
	}

	switch commandParam {
	case "timestamp":
		timestamp, err := getSaveDataTimestamp(uuid)
		if err != nil {
			if err == sql.ErrNoRows {
				return
			}
			handleInternalError(w, r, err)
			return
		}
		w.Write([]byte(timestamp.Format(time.RFC3339)))
		return
	case "get":
		saveData, err := getSaveData(uuid)
		if err != nil {
			if err == sql.ErrNoRows {
				w.Write([]byte("{}"))
				return
			}
			handleInternalError(w, r, err)
			return
		}
		w.Write(saveData)
		return
	case "push":
		data, err := io.ReadAll(r.Body)
		defer r.Body.Close()
		if err != nil || len(data) > 1024*1024*8 {
			handleError(w, r, "invalid data")
			return
		}
		err = createGameSaveData(uuid, data)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		return
	case "clear":
		err := clearGameSaveData(uuid)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
	default:
		handleError(w, r, "unknown command")
		return
	}

	w.Write([]byte("ok"))
}

func handleVm(w http.ResponseWriter, r *http.Request) {
	idParam := r.URL.Query().Get("id")
	if idParam == "" {
		handleError(w, r, "id not specified")
		return
	}

	eventVmId, err := strconv.Atoi(idParam)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	mapId, eventId, err := getEventVmInfo(eventVmId)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	fileBytes, err := os.ReadFile("vms/Map" + fmt.Sprintf("%04d", mapId) + "_EV" + fmt.Sprintf("%04d", eventId) + ".png")
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(fileBytes)
}

func handleBadge(w http.ResponseWriter, r *http.Request) {
	var uuid string
	var name string
	var rank int
	var badge string
	var badgeSlotRows int
	var badgeSlotCols int
	var banned bool

	commandParam := r.URL.Query().Get("command")
	if commandParam == "" {
		handleError(w, r, "command not specified")
		return
	}
	token := r.Header.Get("Authorization")
	if token == "" {
		if commandParam == "list" || commandParam == "playerSlotList" {
			uuid, banned, _ = getOrCreatePlayerData(getIp(r))
		} else {
			handleError(w, r, "token not specified")
			return
		}
	} else {
		uuid, name, rank, badge, banned, _ = getPlayerDataFromToken(token)
		if uuid == "" {
			handleError(w, r, "invalid token")
			return
		}
	}

	if banned {
		handleError(w, r, "player is banned")
		return
	}

	if strings.HasPrefix(commandParam, "slot") {
		badgeSlotRows, badgeSlotCols = getPlayerBadgeSlotCounts(name)
	}

	switch commandParam {
	case "set", "slotSet":
		idParam := r.URL.Query().Get("id")
		if idParam == "" {
			handleError(w, r, "id not specified")
			return
		}

		if idParam != badge {
			var unlocked bool

			switch idParam {
			case "null":
				unlocked = true
			default:
				tags, _, err := getPlayerTags(uuid)
				if err != nil {
					handleInternalError(w, r, err)
					return
				}
				badgeData, err := getPlayerBadgeData(uuid, rank, tags, true, true)
				if err != nil {
					handleInternalError(w, r, err)
					return
				}
				var badgeFound bool
				for _, badge := range badgeData {
					if badge.BadgeId == idParam {
						badgeFound = true
						unlocked = badge.Unlocked
						break
					}
				}
				if !badgeFound {
					handleError(w, r, "unknown badge")
					return
				}
			}

			if rank < 2 && !unlocked {
				handleError(w, r, "specified badge is locked")
				return
			}
		}

		if commandParam == "set" {
			err := setPlayerBadge(uuid, idParam)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
		} else {
			rowParam := r.URL.Query().Get("row")
			if rowParam == "" {
				handleError(w, r, "row not specified")
				return
			}

			colParam := r.URL.Query().Get("col")
			if colParam == "" {
				handleError(w, r, "col not specified")
				return
			}

			slotRow, err := strconv.Atoi(rowParam)
			if err != nil || slotRow == 0 || slotRow > badgeSlotRows {
				handleError(w, r, "invalid row value")
				return
			}

			slotCol, err := strconv.Atoi(colParam)
			if err != nil || slotCol == 0 || slotCol > badgeSlotCols {
				handleError(w, r, "invalid col value")
				return
			}

			err = setPlayerBadgeSlot(uuid, idParam, slotRow, slotCol)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
		}
	case "list":
		var tags []string
		if token != "" {
			var err error
			tags, _, err = getPlayerTags(uuid)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
		}
		if r.URL.Query().Get("simple") == "true" {
			simpleBadgeData, err := getSimplePlayerBadgeData(uuid, rank, tags, token != "")
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			simpleBadgeDataJson, err := json.Marshal(simpleBadgeData)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			w.Write(simpleBadgeDataJson)
		} else {
			if token == "" {
				handleError(w, r, "cannot retrieve player badge data for guest player")
				return
			}
			badgeData, err := getPlayerBadgeData(uuid, rank, tags, true, false)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			badgeDataJson, err := json.Marshal(badgeData)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			w.Write(badgeDataJson)
		}
		return
	case "new":
		since := r.URL.Query().Get("since")
		sinceTimestamp, err := time.Parse(time.RFC3339, since)
		if err != nil {
			sinceTimestamp = time.Time{}
		}
		var tags []string
		var newTags bool
		if token != "" {
			var err error
			var lastUnlocked time.Time
			tags, lastUnlocked, err = getPlayerTags(uuid)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
			newTags = lastUnlocked.UTC().After(sinceTimestamp)
		}
		newUnlockedBadgeIds, err := getPlayerNewUnlockedBadgeIds(uuid, rank, tags)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if len(newUnlockedBadgeIds) != 0 {
			err := updatePlayerBadgeSlotCounts(uuid)
			if err != nil {
				handleInternalError(w, r, err)
				return
			}
		}
		response := &CheckUpdateData{BadgeIds: newUnlockedBadgeIds, NewTags: newTags}
		responseJson, err := json.Marshal(response)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		w.Write(responseJson)
		return
	case "slotList":
		badgeSlots, err := getPlayerBadgeSlots(name, badgeSlotRows, badgeSlotCols)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		badgeSlotsJson, err := json.Marshal(badgeSlots)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		w.Write(badgeSlotsJson)
		return
	case "playerSlotList":
		playerParam := r.URL.Query().Get("player")
		if playerParam == "" {
			handleError(w, r, "player not specified")
			return
		}

		playerBadgeSlotRows, playerBadgeSlotCols := getPlayerBadgeSlotCounts(playerParam)

		badgeSlots, err := getPlayerBadgeSlots(playerParam, playerBadgeSlotRows, playerBadgeSlotCols)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		badgeSlotsJson, err := json.Marshal(badgeSlots)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		w.Write(badgeSlotsJson)
		return
	default:
		handleError(w, r, "unknown command")
		return
	}

	w.Write([]byte("ok"))
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	user, password := r.Form.Get("user"), r.Form.Get("password")

	if user == "" || len(user) > 12 || !isOkString(user) || password == "" || len(password) > 72 {
		handleError(w, r, "bad response")
		return
	}

	ip := getIp(r)

	if isIpBanned(ip) {
		handleError(w, r, "banned users cannot create accounts")
		return
	}

	var userExists int
	db.QueryRow("SELECT EXISTS(SELECT * FROM accounts WHERE user = ?)", user).Scan(&userExists)

	if userExists > 0 {
		handleError(w, r, "user exists")
		return
	}

	var uuid string
	db.QueryRow("SELECT uuid FROM players WHERE ip = ?", ip).Scan(&uuid) // no row causes a non-fatal error, uuid is still unset so it doesn't matter
	if uuid == "" {
		uuid, _, _ = getOrCreatePlayerData(ip)
	}

	db.Exec("UPDATE players SET ip = NULL WHERE ip = ?", ip) // set ip to null to disable ip-based login

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		handleError(w, r, "bcrypt error")
		return
	}

	db.Exec("INSERT INTO accounts (ip, timestampRegistered, uuid, user, pass) VALUES (?, NOW(), ?, ?, ?)", ip, uuid, user, hashedPassword)

	w.Write([]byte("ok"))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	user, password := r.Form.Get("user"), r.Form.Get("password")

	if user == "" || !isOkString(user) || password == "" || len(password) > 72 {
		handleError(w, r, "bad response")
		return
	}

	var userPassHash string
	db.QueryRow("SELECT pass FROM accounts WHERE user = ?", user).Scan(&userPassHash)

	if userPassHash == "" || bcrypt.CompareHashAndPassword([]byte(userPassHash), []byte(password)) != nil {
		handleError(w, r, "bad login")
		return
	}

	token := randString(32)
	db.Exec("INSERT INTO playerSessions (sessionId, uuid, expiration) (SELECT ?, uuid, DATE_ADD(NOW(), INTERVAL 30 DAY) FROM accounts WHERE user = ?)", token, user)
	db.Exec("UPDATE accounts SET timestampLoggedIn = NOW() WHERE user = ?", user)

	w.Write([]byte(token))
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	if getUuidFromToken(token) == "" {
		handleError(w, r, "invalid token")
		return
	}

	db.Exec("DELETE FROM playerSessions WHERE sessionId = ?", token)

	w.Write([]byte("ok"))
}

func handleChangePw(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	_, loginUser, rank, _, _, _, _ := getPlayerInfoFromToken(token)

	// GET params user, new password
	user, newPassword := r.URL.Query().Get("user"), r.URL.Query().Get("newPassword")

	var username string
	if rank < 1 || user == "" {
		username = loginUser

		// GET param password
		password := r.URL.Query().Get("password")

		if username == "" || !isOkString(username) || password == "" || len(password) > 72 || newPassword == "" || len(newPassword) > 72 {
			handleError(w, r, "bad response")
			return
		}

		var userPassHash string
		db.QueryRow("SELECT pass FROM accounts WHERE user = ?", username).Scan(&userPassHash)

		if userPassHash == "" || bcrypt.CompareHashAndPassword([]byte(userPassHash), []byte(password)) != nil {
			handleError(w, r, "bad login")
			return
		}
	} else {
		if !isOkString(user) || newPassword == "" || len(newPassword) > 72 {
			handleError(w, r, "bad response")
			return
		}

		username = user
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		handleError(w, r, "bcrypt error")
		return
	}

	db.Exec("UPDATE accounts SET pass = ? WHERE user = ?", hashedPassword, username)

	w.Write([]byte("ok"))
}

func handleResetPw(uuid string) (newPassword string, err error) {
	var userExists int
	db.QueryRow("SELECT EXISTS (SELECT * FROM accounts WHERE uuid = ?)", uuid).Scan(&userExists)

	if userExists == 0 {
		return "", errors.New("user not found")
	}

	newPassword = randString(8)

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", errors.New("bcrypt error")
	}

	db.Exec("UPDATE accounts SET pass = ? WHERE uuid = ?", hashedPassword, uuid)

	return newPassword, nil
}

func handleAddPlayerFriend(w http.ResponseWriter, r *http.Request) {
	handleAddRemovePlayerFriend(w, r, true)
}

func handleRemovePlayerFriend(w http.ResponseWriter, r *http.Request) {
	handleAddRemovePlayerFriend(w, r, false)
}

func handleAddRemovePlayerFriend(w http.ResponseWriter, r *http.Request, isAdd bool) {
	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	uuid := getUuidFromToken(token)

	if uuid == "" {
		handleError(w, r, "invalid token")
		return
	}

	targetUuid := r.URL.Query().Get("uuid")
	if targetUuid == "" {
		user := r.URL.Query().Get("user")
		if user == "" {
			handleError(w, r, "uuid or user not specified")
			return
		}

		uuid, err := getUuidFromName(user)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}

		if uuid == "" {
			handleError(w, r, "invalid user specified")
			return
		}

		targetUuid = uuid
	}

	var err error

	if isAdd {
		err = addPlayerFriend(uuid, targetUuid)
	} else {
		err = removePlayerFriend(uuid, targetUuid)
	}

	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte("ok"))
}

func handleBlockPlayer(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	var uuid string

	if token == "" {
		uuid, _, _ = getPlayerInfo(getIp(r))
	} else {
		uuid = getUuidFromToken(token)
	}

	targetUuid := r.URL.Query().Get("uuid")
	if targetUuid == "" {
		user := r.URL.Query().Get("user")
		if user == "" {
			handleError(w, r, "uuid or user not specified")
			return
		}

		uuid, err := getUuidFromName(user)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}

		if uuid == "" {
			handleError(w, r, "invalid user specified")
			return
		}

		targetUuid = uuid
	}

	err := tryBlockPlayer(uuid, targetUuid)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	// "disconnect" them NOW!!!
	if client, ok := clients.Load(uuid); ok {
		if otherClient, ok := clients.Load(targetUuid); ok {
			if (client.roomC != nil && otherClient.roomC != nil) && client.roomC.room == otherClient.roomC.room {
				client.roomC.outbox <- buildMsg("d", otherClient.id)
				otherClient.roomC.outbox <- buildMsg("d", client.id)
			}
		}
	}

	w.Write([]byte("ok"))
}

func handleUnblockPlayer(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	var uuid string

	if token == "" {
		uuid, _, _ = getPlayerInfo(getIp(r))
	} else {
		uuid = getUuidFromToken(token)
	}

	targetUuid := r.URL.Query().Get("uuid")
	if targetUuid == "" {
		user := r.URL.Query().Get("user")
		if user == "" {
			handleError(w, r, "uuid or user not specified")
			return
		}

		uuid, err := getUuidFromName(user)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}

		if uuid == "" {
			handleError(w, r, "invalid user specified")
			return
		}

		targetUuid = uuid
	}

	err := tryUnblockPlayer(uuid, targetUuid)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	// "connect" them NOW!!!
	if client, ok := clients.Load(uuid); ok {
		if otherClient, ok := clients.Load(targetUuid); ok {
			if (client.roomC != nil && otherClient.roomC != nil) && client.roomC.room == otherClient.roomC.room {
				client.roomC.getPlayerData(otherClient.roomC)
				otherClient.roomC.getPlayerData(client.roomC)
			}
		}
	}

	w.Write([]byte("ok"))
}

func handleBlockList(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	var uuid string

	if token == "" {
		uuid, _, _ = getPlayerInfo(getIp(r))
	} else {
		uuid = getUuidFromToken(token)
	}

	blockedPlayers, err := getBlockedPlayerData(uuid)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	// for hiding blocked players
	if client, ok := clients.Load(uuid); ok {
		blockedUsers := make(map[string]bool)

		for _, player := range blockedPlayers {
			blockedUsers[player.Uuid] = true
		}

		client.blockedUsers = blockedUsers
	}

	blockedPlayersJson, err := json.Marshal(blockedPlayers)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write(blockedPlayersJson)
}

func handleExplorer(w http.ResponseWriter, r *http.Request) {
	if config.gameName != "2kki" {
		handleError(w, r, "explorer is only available for Yume 2kki")
		return
	}

	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	uuid := getUuidFromToken(token)

	if client, ok := clients.Load(uuid); ok {
		if client.roomC != nil {
			var allConnLocationNames []string
			retUrl := "https://2kki.app/location?locations="

			for i, locationName := range client.roomC.locations {
				var connLocationNames []string

				if i > 0 {
					retUrl += "|"
				}
				retUrl += url.QueryEscape(locationName)

				getConnectionsUrl := "https://2kki.app/getConnectedLocations?locationName=" + url.QueryEscape(locationName)
				resp, err := http.Get(getConnectionsUrl)
				if err != nil {
					writeErrLog(getIp(r), r.URL.Path, err.Error())
					continue
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					writeErrLog(getIp(r), r.URL.Path, err.Error())
					continue
				}

				if strings.HasPrefix(string(body), "{\"error\"") {
					writeErrLog(getIp(r), r.URL.Path, "Invalid 2kki location info: "+string(body))
					continue
				}

				err = json.Unmarshal(body, &connLocationNames)
				if err != nil {
					writeErrLog(getIp(r), r.URL.Path, err.Error())
					continue
				}

				allConnLocationNames = append(allConnLocationNames, connLocationNames...)
			}

			hiddenLocationNames, err := getPlayerMissingGameLocationNames(uuid, allConnLocationNames)
			if err != nil {
				handleError(w, r, err.Error())
				return
			}

			if len(hiddenLocationNames) > 0 {
				retUrl += "&hiddenConnLocations="

				for i, hiddenLocationName := range hiddenLocationNames {
					if i > 0 {
						retUrl += "|"
					}
					retUrl += url.QueryEscape(hiddenLocationName)
				}
			}

			trackedLocations := r.URL.Query().Get("trackedLocations")
			if trackedLocations != "" {
				retUrl += "&trackedConnLocations=" + trackedLocations
			}

			w.Write([]byte(retUrl))
		}
	}

	w.Write([]byte(""))
}

func handleExplorerCompletion(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	uuid := getUuidFromToken(token)

	locationCompletion, err := getPlayerGameLocationCompletion(uuid, config.gameName)
	if err != nil {
		handleError(w, r, err.Error())
		return
	}

	w.Write([]byte(strconv.Itoa(locationCompletion)))
}

func handleExplorerLocations(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")

	if token == "" {
		handleError(w, r, "token not specified")
		return
	}

	uuid := getUuidFromToken(token)

	locationCompletion, err := getPlayerGameLocationCompletion(uuid, config.gameName)
	if err != nil {
		handleError(w, r, err.Error())
		return
	}

	if locationCompletion < 95 {
		return
	}

	missingLocationNames, err := getPlayerAllMissingGameLocationNames(uuid)
	if err != nil {
		handleError(w, r, err.Error())
		return
	}

	missingLocationNamesJson, err := json.Marshal(missingLocationNames)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte(missingLocationNamesJson))
}

func handleError(w http.ResponseWriter, r *http.Request, payload string) {
	writeErrLog(getIp(r), r.URL.Path, payload)
	http.Error(w, payload, http.StatusBadRequest)
}

func handleInternalError(w http.ResponseWriter, r *http.Request, err error) {
	writeErrLog(getIp(r), r.URL.Path, err.Error())
	http.Error(w, "400 - Bad Request", http.StatusBadRequest)
}

func handleChatHistory(w http.ResponseWriter, r *http.Request) {
	var uuid string

	token := r.Header.Get("Authorization")

	if token == "" {
		uuid, _, _ = getOrCreatePlayerData(getIp(r))
	} else {
		uuid = getUuidFromToken(token)
	}

	lastMsgId := r.URL.Query().Get("lastMsgId")

	if lastMsgId != "" && len(lastMsgId) != 12 {
		handleError(w, r, "invalid lastMsgId")
		return
	}

	globalMsgLimitParam := r.URL.Query().Get("globalMsgLimit")
	if globalMsgLimitParam == "" {
		globalMsgLimitParam = "100"
	}

	partyMsgLimitParam := r.URL.Query().Get("partyMsgLimit")
	if partyMsgLimitParam == "" {
		partyMsgLimitParam = "250"
	}

	globalMsgLimit, err := strconv.Atoi(globalMsgLimitParam)
	if err != nil {
		handleError(w, r, "invalid globalMsgLimit value")
		return
	}

	partyMsgLimit, err := strconv.Atoi(partyMsgLimitParam)
	if err != nil {
		handleError(w, r, "invalid partyMsgLimit value")
		return
	}

	if globalMsgLimit <= 0 || globalMsgLimit > 100 {
		globalMsgLimit = 100
	}

	if partyMsgLimit <= 0 || partyMsgLimit > 250 {
		partyMsgLimit = 250
	}

	chatHistory, err := getChatMessageHistory(uuid, globalMsgLimit, partyMsgLimit, lastMsgId)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	chatHistoryJson, err := json.Marshal(chatHistory)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write(chatHistoryJson)
}

func handleClearChatHistory(w http.ResponseWriter, r *http.Request) {
	var uuid string

	token := r.Header.Get("Authorization")

	if token == "" {
		uuid, _, _ = getOrCreatePlayerData(getIp(r))
	} else {
		uuid = getUuidFromToken(token)
	}

	lastGlobalMsgId := r.URL.Query().Get("lastGlobalMsgId")
	if lastGlobalMsgId != "" {
		if len(lastGlobalMsgId) != 12 {
			handleError(w, r, "invalid lastGlobalMsgId")
			return
		}

		updatePlayerLastChatMessage(uuid, lastGlobalMsgId, false)
	}

	lastPartyMsgId := r.URL.Query().Get("lastPartyMsgId")
	if lastPartyMsgId != "" {
		if len(lastPartyMsgId) != 12 {
			handleError(w, r, "invalid lastPartyMsgId")
			return
		}

		updatePlayerLastChatMessage(uuid, lastPartyMsgId, true)
	}

	w.Write([]byte("ok"))
}

func handle2kki(w http.ResponseWriter, r *http.Request) {
	if config.gameName != "2kki" {
		handleError(w, r, "endpoint not supported")
		return
	}

	actionParam := r.URL.Query().Get("action")
	if actionParam == "" {
		handleError(w, r, "action not specified")
		return
	}

	query := r.URL.Query()
	query.Del("action")

	queryString := query.Encode()

	response, err := query2kki(actionParam, queryString)
	if err != nil {
		if response == "" {
			handleInternalError(w, r, err)
		} else {
			writeErrLog(getIp(r), r.URL.Path, err.Error())
		}
	}

	w.Write([]byte(response))
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	var uuid string
	var name string
	var rank int
	var badge string
	var badgeSlotRows int
	var badgeSlotCols int
	var screenshotLimit int
	var medals [5]int
	var locationIds []int

	var err error

	token := r.Header.Get("Authorization")
	if token == "" {
		uuid, name, rank = getPlayerInfo(getIp(r))
	} else {
		uuid, name, rank, badge, badgeSlotRows, badgeSlotCols, screenshotLimit = getPlayerInfoFromToken(token)
		medals = getPlayerMedals(uuid)
		locationIds, _ = getPlayerGameLocationIds(uuid, config.gameName)
	}

	// guest accounts with no playerGameData records will return nothing
	// if uuid is empty it breaks fetchAndUpdatePlayerInfo in forest-orb
	if uuid == "" {
		uuid = "null"
	}

	playerInfo := PlayerInfo{
		Uuid:            uuid,
		Name:            name,
		Rank:            rank,
		Badge:           badge,
		BadgeSlotRows:   badgeSlotRows,
		BadgeSlotCols:   badgeSlotCols,
		ScreenshotLimit: screenshotLimit,
		Medals:          medals,
		LocationIds:     locationIds,
	}
	playerInfoJson, err := json.Marshal(playerInfo)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}
	w.Write(playerInfoJson)
}

func handlePlayers(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(strconv.Itoa(clients.GetAmount())))
}

func query2kki(action string, queryString string) (response string, err error) {
	err = db.QueryRow("SELECT response FROM 2kkiApiQueries WHERE action = ? AND query = ? AND NOW() < timestampExpired", action, queryString).Scan(&response)
	if err != nil {
		if err != sql.ErrNoRows {
			return "", err
		}

		url := "https://2kki.app/" + action
		if queryString != "" {
			url += "?" + queryString
		}

		resp, err := http.Get(url)
		if err != nil {
			return "", err
		}

		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		if strings.HasPrefix(string(body), "{\"error\"") || strings.HasPrefix(string(body), "<!DOCTYPE html>") {
			return string(body), errors.New("received error response from Yume 2kki Explorer API: " + string(body))
		} else {
			_, err = db.Exec("INSERT INTO 2kkiApiQueries (action, query, response, timestampExpired) VALUES (?, ?, ?, DATE_ADD(NOW(), INTERVAL 1 HOUR)) ON DUPLICATE KEY UPDATE response = ?, timestampExpired = DATE_ADD(NOW(), INTERVAL 1 HOUR)", action, queryString, string(body), string(body))
			if err != nil {
				return "", err
			}
		}

		return string(body), nil
	}

	return response, nil
}

func queryWiki(action string, queryString string) (response string, err error) {
	err = db.QueryRow("SELECT response FROM wikiApiQueries WHERE game = ? AND action = ? AND query = ? AND NOW() < timestampExpired", config.gameName, action, queryString).Scan(&response)
	if err != nil {
		if err != sql.ErrNoRows {
			return "", err
		}

		url := "https://wrapper.yume.wiki/" + action + "?game=" + config.gameName
		if queryString != "" {
			url += "&" + queryString
		}

		var resp *http.Response
		resp, err = http.Get(url)
		if err != nil {
			return "", err
		}

		defer resp.Body.Close()

		var body []byte
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		bodyStr := string(body)

		if strings.HasPrefix(bodyStr, "{\"error\"") || strings.HasPrefix(bodyStr, "<!DOCTYPE html>") {
			return "", errors.New("received error response from Yume Wiki API: " + bodyStr)
		} else {
			_, err = db.Exec("INSERT INTO wikiApiQueries (game, action, query, response, timestampExpired) VALUES (?, ?, ?, ?, DATE_ADD(NOW(), INTERVAL 1 HOUR)) ON DUPLICATE KEY UPDATE response = ?, timestampExpired = DATE_ADD(NOW(), INTERVAL 12 HOUR)", config.gameName, action, queryString, bodyStr, bodyStr)
			if err != nil {
				return "", err
			}
		}

		return bodyStr, nil
	}

	return response, nil
}
