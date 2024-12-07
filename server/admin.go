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
	"encoding/json"
	"net/http"
)

func adminGetPlayers(w http.ResponseWriter, r *http.Request) {
	_, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}

	response := make([]PlayerInfo, 0, clients.GetAmount())
	for _, client := range clients.Get() {
		response = append(response, PlayerInfo{
			Uuid: client.uuid,
			Name: client.name,
			Rank: client.rank,
		})
	}

	responseJson, err := json.Marshal(response)
	if err != nil {
		handleError(w, r, "error while marshaling")
		return
	}

	w.Write(responseJson)
}

func adminGetBansMutes(w http.ResponseWriter, r *http.Request) {
	_, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}
	
	responseJson, err := json.Marshal(getBannedMutedPlayers(r.URL.Path == "/admin/getbans"))
	if err != nil {
		handleError(w, r, "error while marshaling")
		return
	}

	w.Write(responseJson)
}

func adminBanMute(w http.ResponseWriter, r *http.Request) {
	uuid, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
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
	switch r.URL.Path {
	case "/admin/ban":
		err = tryBanPlayer(uuid, targetUuid)
	case "/admin/unban":
		err = tryUnbanPlayer(uuid, targetUuid)
	case "/admin/mute":
		err = tryMutePlayer(uuid, targetUuid)
	case "/admin/unmute":
		err = tryUnmutePlayer(uuid, targetUuid)
	}
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte("ok"))
}

func adminChangeUsername(w http.ResponseWriter, r *http.Request) {
	uuid, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}

	user, newUser := r.URL.Query().Get("user"), r.URL.Query().Get("newUser")

	if user == "" {
		handleError(w, r, "user not specified")
		return
	}

	if newUser == "" {
		handleError(w, r, "new username not specified")
		return
	}

	userUuid, err := getUuidFromName(user)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}
	if userUuid == "" {
		handleError(w, r, "invalid user specified")
		return
	}

	err = tryChangePlayerUsername(uuid, userUuid, newUser)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte("ok"))
}

func adminTempBanUser(w http.ResponseWriter, r *http.Request) {
	uuid, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}

	targetUuid, duration := r.URL.Query().Get("uuid"), r.URL.Query().Get("duration")
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
	
	if duration = 0 {
		handleError(w, r, "duration not specified")
		return
	}

	duration , err := handleTempBan(uuid, targetUuid)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte(duration))
}

func adminResetPw(w http.ResponseWriter, r *http.Request) {
	_, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}

	user := r.URL.Query().Get("user")

	if user == "" {
		handleError(w, r, "user not specified")
		return
	}

	userUuid, err := getUuidFromName(user)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}
	if userUuid == "" {
		handleError(w, r, "invalid user specified")
		return
	}

	newPw, err := handleResetPw(userUuid)
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte(newPw))
}

func adminManageBadge(w http.ResponseWriter, r *http.Request) {
	_, _, rank, _, _, _ := getPlayerDataFromToken(r.Header.Get("Authorization"))
	if rank == 0 {
		handleError(w, r, "access denied")
		return
	}

	uuidParam := r.URL.Query().Get("uuid")
	if uuidParam == "" {
		userParam := r.URL.Query().Get("user")
		if userParam == "" {
			handleError(w, r, "uuid or user not specified")
			return
		}
		var err error
		uuidParam, err = getUuidFromName(userParam)
		if err != nil {
			handleInternalError(w, r, err)
			return
		}
		if uuidParam == "" {
			handleError(w, r, "invalid user specified")
			return
		}
	}

	idParam := r.URL.Query().Get("id")
	if idParam == "" {
		handleError(w, r, "badge ID not specified")
		return
	}

	var badgeExists bool

	for _, gameBadges := range badges {
		for badgeId := range gameBadges {
			if badgeId == idParam {
				badgeExists = true
				break
			}
		}
		if badgeExists {
			break
		}
	}

	if !badgeExists {
		handleError(w, r, "badge not found for the provided badge ID")
		return
	}

	var err error
	if r.URL.Path == "/admin/grantbadge" {
		err = unlockPlayerBadge(uuidParam, idParam)
	} else {
		err = removePlayerBadge(uuidParam, idParam)
	}
	if err != nil {
		handleInternalError(w, r, err)
		return
	}

	w.Write([]byte("ok"))
}
