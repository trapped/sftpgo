package httpd

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi"
	"github.com/go-chi/render"

	"github.com/drakkan/sftpgo/common"
	"github.com/drakkan/sftpgo/dataprovider"
	"github.com/drakkan/sftpgo/kms"
	"github.com/drakkan/sftpgo/vfs"
)

func getUsers(w http.ResponseWriter, r *http.Request) {
	limit := 100
	offset := 0
	order := dataprovider.OrderASC
	username := ""
	var err error
	if _, ok := r.URL.Query()["limit"]; ok {
		limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil {
			err = errors.New("Invalid limit")
			sendAPIResponse(w, r, err, "", http.StatusBadRequest)
			return
		}
		if limit > 500 {
			limit = 500
		}
	}
	if _, ok := r.URL.Query()["offset"]; ok {
		offset, err = strconv.Atoi(r.URL.Query().Get("offset"))
		if err != nil {
			err = errors.New("Invalid offset")
			sendAPIResponse(w, r, err, "", http.StatusBadRequest)
			return
		}
	}
	if _, ok := r.URL.Query()["order"]; ok {
		order = r.URL.Query().Get("order")
		if order != dataprovider.OrderASC && order != dataprovider.OrderDESC {
			err = errors.New("Invalid order")
			sendAPIResponse(w, r, err, "", http.StatusBadRequest)
			return
		}
	}
	if _, ok := r.URL.Query()["username"]; ok {
		username = r.URL.Query().Get("username")
	}
	users, err := dataprovider.GetUsers(limit, offset, order, username)
	if err == nil {
		render.JSON(w, r, users)
	} else {
		sendAPIResponse(w, r, err, "", http.StatusInternalServerError)
	}
}

func getUserByID(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		err = errors.New("Invalid userID")
		sendAPIResponse(w, r, err, "", http.StatusBadRequest)
		return
	}
	user, err := dataprovider.GetUserByID(userID)
	if err == nil {
		user.HideConfidentialData()
		render.JSON(w, r, user)
	} else {
		sendAPIResponse(w, r, err, "", getRespStatus(err))
	}
}

func addUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)
	var user dataprovider.User
	err := render.DecodeJSON(r.Body, &user)
	if err != nil {
		sendAPIResponse(w, r, err, "", http.StatusBadRequest)
		return
	}
	user.SetEmptySecretsIfNil()
	switch user.FsConfig.Provider {
	case dataprovider.S3FilesystemProvider:
		if user.FsConfig.S3Config.AccessSecret.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid access_secret"), "", http.StatusBadRequest)
			return
		}
	case dataprovider.GCSFilesystemProvider:
		if user.FsConfig.GCSConfig.Credentials.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid credentials"), "", http.StatusBadRequest)
			return
		}
	case dataprovider.AzureBlobFilesystemProvider:
		if user.FsConfig.AzBlobConfig.AccountKey.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid account_key"), "", http.StatusBadRequest)
			return
		}
	case dataprovider.CryptedFilesystemProvider:
		if user.FsConfig.CryptConfig.Passphrase.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid passphrase"), "", http.StatusBadRequest)
			return
		}
	case dataprovider.SFTPFilesystemProvider:
		if user.FsConfig.SFTPConfig.Password.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid SFTP password"), "", http.StatusBadRequest)
			return
		}
		if user.FsConfig.SFTPConfig.PrivateKey.IsRedacted() {
			sendAPIResponse(w, r, errors.New("invalid SFTP private key"), "", http.StatusBadRequest)
			return
		}
	}
	err = dataprovider.AddUser(&user)
	if err == nil {
		user, err = dataprovider.UserExists(user.Username)
		if err == nil {
			user.HideConfidentialData()
			render.JSON(w, r, user)
		} else {
			sendAPIResponse(w, r, err, "", getRespStatus(err))
		}
	} else {
		sendAPIResponse(w, r, err, "", getRespStatus(err))
	}
}

func updateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)
	userID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		err = errors.New("Invalid userID")
		sendAPIResponse(w, r, err, "", http.StatusBadRequest)
		return
	}
	disconnect := 0
	if _, ok := r.URL.Query()["disconnect"]; ok {
		disconnect, err = strconv.Atoi(r.URL.Query().Get("disconnect"))
		if err != nil {
			err = fmt.Errorf("invalid disconnect parameter: %v", err)
			sendAPIResponse(w, r, err, "", http.StatusBadRequest)
			return
		}
	}
	user, err := dataprovider.GetUserByID(userID)
	if err != nil {
		sendAPIResponse(w, r, err, "", getRespStatus(err))
		return
	}
	currentPermissions := user.Permissions
	currentS3AccessSecret := user.FsConfig.S3Config.AccessSecret
	currentAzAccountKey := user.FsConfig.AzBlobConfig.AccountKey
	currentGCSCredentials := user.FsConfig.GCSConfig.Credentials
	currentCryptoPassphrase := user.FsConfig.CryptConfig.Passphrase
	currentSFTPPassword := user.FsConfig.SFTPConfig.Password
	currentSFTPKey := user.FsConfig.SFTPConfig.PrivateKey

	user.Permissions = make(map[string][]string)
	user.FsConfig.S3Config = vfs.S3FsConfig{}
	user.FsConfig.AzBlobConfig = vfs.AzBlobFsConfig{}
	user.FsConfig.GCSConfig = vfs.GCSFsConfig{}
	user.FsConfig.CryptConfig = vfs.CryptFsConfig{}
	user.FsConfig.SFTPConfig = vfs.SFTPFsConfig{}
	err = render.DecodeJSON(r.Body, &user)
	if err != nil {
		sendAPIResponse(w, r, err, "", http.StatusBadRequest)
		return
	}
	user.SetEmptySecretsIfNil()
	// we use new Permissions if passed otherwise the old ones
	if len(user.Permissions) == 0 {
		user.Permissions = currentPermissions
	}
	updateEncryptedSecrets(&user, currentS3AccessSecret, currentAzAccountKey, currentGCSCredentials, currentCryptoPassphrase,
		currentSFTPPassword, currentSFTPKey)
	if user.ID != userID {
		sendAPIResponse(w, r, err, "user ID in request body does not match user ID in path parameter", http.StatusBadRequest)
		return
	}
	err = dataprovider.UpdateUser(&user)
	if err != nil {
		sendAPIResponse(w, r, err, "", getRespStatus(err))
	} else {
		sendAPIResponse(w, r, err, "User updated", http.StatusOK)
		if disconnect == 1 {
			disconnectUser(user.Username)
		}
	}
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
	if err != nil {
		err = errors.New("Invalid userID")
		sendAPIResponse(w, r, err, "", http.StatusBadRequest)
		return
	}
	user, err := dataprovider.GetUserByID(userID)
	if err != nil {
		sendAPIResponse(w, r, err, "", getRespStatus(err))
		return
	}
	err = dataprovider.DeleteUser(&user)
	if err != nil {
		sendAPIResponse(w, r, err, "", http.StatusInternalServerError)
	} else {
		sendAPIResponse(w, r, err, "User deleted", http.StatusOK)
		disconnectUser(user.Username)
	}
}

func disconnectUser(username string) {
	for _, stat := range common.Connections.GetStats() {
		if stat.Username == username {
			common.Connections.Close(stat.ConnectionID)
		}
	}
}

func updateEncryptedSecrets(user *dataprovider.User, currentS3AccessSecret, currentAzAccountKey,
	currentGCSCredentials, currentCryptoPassphrase, currentSFTPPassword, currentSFTPKey *kms.Secret) {
	// we use the new access secret if plain or empty, otherwise the old value
	switch user.FsConfig.Provider {
	case dataprovider.S3FilesystemProvider:
		if user.FsConfig.S3Config.AccessSecret.IsNotPlainAndNotEmpty() {
			user.FsConfig.S3Config.AccessSecret = currentS3AccessSecret
		}
	case dataprovider.AzureBlobFilesystemProvider:
		if user.FsConfig.AzBlobConfig.AccountKey.IsNotPlainAndNotEmpty() {
			user.FsConfig.AzBlobConfig.AccountKey = currentAzAccountKey
		}
	case dataprovider.GCSFilesystemProvider:
		if user.FsConfig.GCSConfig.Credentials.IsNotPlainAndNotEmpty() {
			user.FsConfig.GCSConfig.Credentials = currentGCSCredentials
		}
	case dataprovider.CryptedFilesystemProvider:
		if user.FsConfig.CryptConfig.Passphrase.IsNotPlainAndNotEmpty() {
			user.FsConfig.CryptConfig.Passphrase = currentCryptoPassphrase
		}
	case dataprovider.SFTPFilesystemProvider:
		if user.FsConfig.SFTPConfig.Password.IsNotPlainAndNotEmpty() {
			user.FsConfig.SFTPConfig.Password = currentSFTPPassword
		}
		if user.FsConfig.SFTPConfig.PrivateKey.IsNotPlainAndNotEmpty() {
			user.FsConfig.SFTPConfig.PrivateKey = currentSFTPKey
		}
	}
}
