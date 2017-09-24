package plugins

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"errors"

	"github.com/Seklfreak/Robyul2/cache"
	"github.com/Seklfreak/Robyul2/helpers"
	"github.com/Seklfreak/Robyul2/metrics"
	"github.com/Seklfreak/Robyul2/models"
	"github.com/bradfitz/slice"
	"github.com/bwmarrin/discordgo"
	"github.com/dustin/go-humanize"
	"github.com/getsentry/raven-go"
	redisCache "github.com/go-redis/cache"
	rethink "github.com/gorethink/gorethink"
	"github.com/vmihailenco/msgpack"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

type RandomPictures struct{}

type DB_RandomPictures_Source struct {
	ID               string   `gorethink:"id,omitempty"`
	GuildID          string   `gorethink:"guildid"`
	PostToChannelIDs []string `gorethink:"post_to_channelids"`
	DriveFolderIDs   []string `gorethink:"drive_folderids"`
	Aliases          []string `gorethink:"aliases"`
}

var (
	driveService *drive.Service
)

const (
	driveSearchText       string = "\"%s\" in parents and (mimeType = \"image/gif\" or mimeType = \"image/jpeg\" or mimeType = \"image/png\" or mimeType = \"application/vnd.google-apps.folder\")"
	driveFieldsText       string = "nextPageToken, files(id, size, mimeType)"
	driveFieldsSingleText string = "id, name, size, modifiedTime, imageMediaMetadata, webContentLink"
	imgurApiUploadBaseUrl string = "https://api.imgur.com/3/image"
)

func (rp *RandomPictures) Commands() []string {
	return []string{
		"randompictures",
		"rapi",
		"rp",
		"pic",
	}
}

func (rp *RandomPictures) Init(session *discordgo.Session) {
	// Set up Google Drive Client
	ctx := context.Background()
	authJson, err := ioutil.ReadFile(helpers.GetConfig().Path("google.client_credentials_json_location").Data().(string))
	helpers.Relax(err)
	config, err := google.JWTConfigFromJSON(authJson, drive.DriveReadonlyScope)
	helpers.Relax(err)
	client := config.Client(ctx)
	driveService, err = drive.New(client)
	helpers.Relax(err)
	// initial random generator
	rand.Seed(time.Now().Unix())

	go func() {
		log := cache.GetLogger()

		defer helpers.Recover()

		for {
			var marshalled []byte
			redisClient := cache.GetRedisClient()

			var rpSources []DB_RandomPictures_Source
			cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
			helpers.Relax(err)
			err = cursor.All(&rpSources)
			helpers.Relax(err)
			if err != nil && err != rethink.ErrEmptyResult {
				raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
				continue
			}

			log.WithField("module", "randompictures").Info("gathering google drive picture cache")
			for _, sourceEntry := range rpSources {
				var key1 string
				var key2 string
				var fileHash string
				var i int
				var entry *drive.File
				for i, entry = range rp.getFileCache(sourceEntry) {
					fileHash = rp.GetFileHash(sourceEntry.ID, entry.Id)
					key1 = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-n:%s:entry:%d", sourceEntry.ID, i+1)
					key2 = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-hash:%s", fileHash)
					marshalled, err = msgpack.Marshal(entry)
					if err != nil {
						raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
						continue
					}
					err = redisClient.Set(key1, fileHash, 7*24*time.Hour).Err()
					if err != nil {
						raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
						continue
					}
					err = redisClient.Set(key2, marshalled, 7*24*time.Hour).Err()
					if err != nil {
						raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
						continue
					}
				}
				key := fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", sourceEntry.ID, "count")
				err = redisClient.Set(key, i+1, 7*24*time.Hour).Err()
				if err != nil {
					raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
					continue
				}
				rp.updateImagesCachedMetric()
			}

			time.Sleep(12 * time.Hour)
		}
	}()
	cache.GetLogger().WithField("module", "randompictures").Info("Started files cache loop (12h)")

	go func() {
		defer helpers.Recover()

		for {
			time.Sleep(time.Duration(rand.Intn(30)+60) * time.Minute)

			redisClient := cache.GetRedisClient()

			var rpSources []DB_RandomPictures_Source
			cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
			helpers.Relax(err)
			err = cursor.All(&rpSources)
			helpers.Relax(err)
			if err != nil && err != rethink.ErrEmptyResult {
				helpers.Relax(err)
			}

			var fileHash string
			var key string

			for _, sourceEntry := range rpSources {
				if len(sourceEntry.PostToChannelIDs) > 0 {
					key = fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", sourceEntry.ID, "count")
					pictureCount, err := redisClient.Get(key).Int64()
					if err == nil {
						for _, postToChannelID := range sourceEntry.PostToChannelIDs {
						RetryNewPicture:
							chosenPicN := rand.Intn(int(pictureCount)) + 1
							key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-n:%s:entry:%d", sourceEntry.ID, chosenPicN)
							fileHash = redisClient.Get(key).Val()
							if fileHash == "" {
								continue
							}
							key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-hash:%s", fileHash)
							resultBytes, err := redisClient.Get(key).Bytes()
							if err == nil {
								var gPicture *drive.File
								msgpack.Unmarshal(resultBytes, &gPicture)
								defer helpers.Recover()
								err = rp.postItem(sourceEntry.GuildID, postToChannelID, "", gPicture, sourceEntry.ID, strconv.Itoa(chosenPicN))
								if err != nil {
									if errG, ok := err.(*googleapi.Error); ok {
										if strings.Contains("The download quota for this file has been exceeded", errG.Error()) {
											goto RetryNewPicture
										} else {
											raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
											continue
										}
									} else {
										raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
										continue
									}
								}
							}
						}
					}
				}
			}
		}
	}()
	cache.GetLogger().WithField("module", "randompictures").Info("Started post loop (1h)")

	go rp.setServerFeaturesLoop()
}

func (rp *RandomPictures) setServerFeaturesLoop() {
	defer func() {
		helpers.Recover()

		cache.GetLogger().WithField("module", "randompictures").Error("The setServerFeaturesLoop died. Please investigate! Will be restarted in 60 seconds")
		time.Sleep(60 * time.Second)
		rp.setServerFeaturesLoop()
	}()

	var sourcesBucket []DB_RandomPictures_Source
	var sourcesOnServer []DB_RandomPictures_Source
	var listCursor *rethink.Cursor
	var err error
	var feature models.Rest_Feature_RandomPictures
	var key string
	cacheCodec := cache.GetRedisCacheCodec()
	for {
		listCursor, err = rethink.Table("randompictures_sources").Run(helpers.GetDB())
		if err != nil {
			raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
			time.Sleep(60 * time.Second)
			continue
		}
		defer listCursor.Close()
		err = listCursor.All(&sourcesBucket)
		if err != nil {
			raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
			time.Sleep(60 * time.Second)
			continue
		}

		for _, guild := range cache.GetSession().State.Guilds {
			sourcesOnServer = make([]DB_RandomPictures_Source, 0)
			for _, source := range sourcesBucket {
				if source.GuildID == guild.ID {
					sourcesOnServer = append(sourcesOnServer, source)
				}
			}

			key = fmt.Sprintf(models.Redis_Key_Feature_RandomPictures, guild.ID)
			feature = models.Rest_Feature_RandomPictures{
				Count: len(sourcesOnServer),
			}

			err = cacheCodec.Set(&redisCache.Item{
				Key:        key,
				Object:     feature,
				Expiration: time.Minute * 60,
			})
			if err != nil {
				raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
			}

		}

		time.Sleep(30 * time.Minute)
	}
}

func (rp *RandomPictures) Action(command string, content string, msg *discordgo.Message, session *discordgo.Session) {
	switch command {
	case "pic": // [p]pic [<name>]
		session.ChannelTyping(msg.ChannelID)
		channel, err := helpers.GetChannel(msg.ChannelID)
		helpers.Relax(err)
		postedPic := false

		var rpSources []DB_RandomPictures_Source
		cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
		helpers.Relax(err)
		err = cursor.All(&rpSources)
		helpers.Relax(err)
		if err != nil && err != rethink.ErrEmptyResult {
			helpers.Relax(err)
		}

		initialMessage, err := session.ChannelMessageSend(msg.ChannelID, helpers.GetText("plugins.randompictures.waiting-for-picture"))
		helpers.Relax(err)

		postedPic, err = rp.postRandomItemFromContent(channel, msg, content, initialMessage, rpSources)
		if err != nil || postedPic == false {
			session.ChannelMessageEdit(msg.ChannelID, initialMessage.ID, helpers.GetText("plugins.randompictures.pic-no-picture"))
			if err != nil {
				fmt.Println(err.Error())
			}
		} else {
			isPostingNewPic := false
			err = session.MessageReactionAdd(msg.ChannelID, initialMessage.ID, "🎲")
			if err == nil {
				randomHandler := session.AddHandler(func(session *discordgo.Session, reaction *discordgo.MessageReactionAdd) {
					defer helpers.Recover()

					if reaction.MessageID == initialMessage.ID {
						if reaction.UserID == session.State.User.ID {
							return
						}

						if reaction.UserID == msg.Author.ID && reaction.Emoji.Name == "🎲" && isPostingNewPic == false {
							postedPic, err = rp.postRandomItemFromContent(channel, msg, content, initialMessage, rpSources)
							if err != nil || postedPic == false {
								session.ChannelMessageEdit(msg.ChannelID, initialMessage.ID, helpers.GetText("plugins.randompictures.pic-no-picture"))
							}
							session.MessageReactionRemove(reaction.ChannelID, reaction.MessageID, reaction.Emoji.Name, reaction.UserID)
						}
					}
				})
				time.Sleep(5 * time.Minute)
				randomHandler()
				session.MessageReactionRemove(msg.ChannelID, initialMessage.ID, "🎲", session.State.User.ID)
			}
		}
		return
	default:
		args := strings.Fields(content)
		if len(args) > 0 {
			switch args[0] {
			case "new-config": // [p]randompictures new-config
				helpers.RequireBotAdmin(msg, func() {
					session.ChannelTyping(msg.ChannelID)

					insert := rethink.Table("randompictures_sources").Insert(DB_RandomPictures_Source{})
					_, err := insert.RunWrite(helpers.GetDB())
					helpers.Relax(err)

					_, err = session.ChannelMessageSend(msg.ChannelID, "Created a new entry in the Database. Please fill it manually.")
					helpers.Relax(err)
				})
				return
			case "list": // [p]randompictures list
				helpers.RequireBotAdmin(msg, func() {
					session.ChannelTyping(msg.ChannelID)

					channel, err := helpers.GetChannel(msg.ChannelID)
					helpers.Relax(err)

					var rpSources []DB_RandomPictures_Source
					cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
					helpers.Relax(err)
					err = cursor.All(&rpSources)
					helpers.Relax(err)

					if err == rethink.ErrEmptyResult || len(rpSources) <= 0 {
						session.ChannelMessageSend(msg.ChannelID, helpers.GetTextF("plugins.randompictures.list-no-entries-error"))
						return
					} else if err != nil {
						helpers.Relax(err)
					}

					listText := ":cloud: Sources set up:\n"
					totalSources := 0
					totalCachedImages := int64(0)
					for _, rpSource := range rpSources {
						if rpSource.GuildID != channel.GuildID {
							continue
						}
						rpSourceGuild, err := helpers.GetGuild(rpSource.GuildID)
						helpers.Relax(err)
						cacheText := "No cache yet"
						key := fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", rpSource.ID, "count")
						pictureCount, err := cache.GetRedisClient().Get(key).Int64()
						if err == nil {
							cacheText = fmt.Sprintf("%s images cached", humanize.Comma(pictureCount))
							totalCachedImages += pictureCount
						}

						listText += fmt.Sprintf(":arrow_forward: `%s`: on %s (#%s), %d Aliases, %d Folders, %d Channels, %s\n",
							rpSource.ID, rpSourceGuild.Name, rpSourceGuild.ID, len(rpSource.Aliases), len(rpSource.DriveFolderIDs), len(rpSource.PostToChannelIDs), cacheText)
						totalSources += 1
					}

					if totalSources <= 0 {
						session.ChannelMessageSend(msg.ChannelID, helpers.GetTextF("plugins.randompictures.list-no-entries-error"))
						return
					}

					listText += fmt.Sprintf("Found **%d** Sources in total and **%s** Cached Images.", totalSources, humanize.Comma(int64(totalCachedImages)))

					for _, page := range helpers.Pagify(listText, "\n") {
						_, err = session.ChannelMessageSend(msg.ChannelID, page)
						helpers.Relax(err)
					}
				})
				return
			case "refresh": // [p]randompictures refresh <source id>
				helpers.RequireBotAdmin(msg, func() {
					session.ChannelTyping(msg.ChannelID)
					if len(args) < 2 {
						_, err := session.ChannelMessageSend(msg.ChannelID, helpers.GetText("bot.arguments.too-few"))
						helpers.Relax(err)
						return
					}

					var rpSources []DB_RandomPictures_Source
					cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
					helpers.Relax(err)
					err = cursor.All(&rpSources)
					helpers.Relax(err)

					if err == rethink.ErrEmptyResult || len(rpSources) <= 0 {
						session.ChannelMessageSend(msg.ChannelID, helpers.GetTextF("plugins.randompictures.list-no-entries-error"))
						return
					} else if err != nil {
						helpers.Relax(err)
					}

					for _, rpSource := range rpSources {
						if rpSource.ID == args[1] {
							var fileHash string
							var key string
							var i int
							var entry *drive.File
							var marshalled []byte
							redisClient := cache.GetRedisClient()
							session.ChannelMessageSend(msg.ChannelID, helpers.GetText("plugins.randompictures.refresh-started"))
							for i, entry = range rp.getFileCache(rpSource) {
								key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-n:%s:entry:%d", rpSource.ID, i+1)
								fileHash = rp.GetFileHash(rpSource.ID, entry.Id)
								if fileHash == "" {
									continue
								}
								err = redisClient.Set(key, fileHash, time.Hour*24).Err()
								if err != nil {
									raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
									continue
								}
								key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-hash:%s", fileHash)
								marshalled, err = msgpack.Marshal(entry)
								if err != nil {
									raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
									continue
								}
								err = redisClient.Set(key, marshalled, time.Hour*24).Err()
								if err != nil {
									raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
									continue
								}
							}
							key = fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", rpSource.ID, "count")
							err = redisClient.Set(key, i+1, time.Hour*24).Err()
							if err != nil {
								raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
								continue
							}
							rp.updateImagesCachedMetric()
							_, err := session.ChannelMessageSend(msg.ChannelID, helpers.GetText("plugins.randompictures.refresh-success"))
							helpers.Relax(err)
							return
						}
					}

					_, err = session.ChannelMessageSend(msg.ChannelID, helpers.GetText("plugins.randompictures.refresh-not-found-error"))
					helpers.Relax(err)
					return
				})
			}
		}
	}

}

func (rp *RandomPictures) postRandomItemFromContent(channel *discordgo.Channel, msg *discordgo.Message, content string, initialMessage *discordgo.Message, rpSources []DB_RandomPictures_Source) (bool, error) {
	var matchEntry DB_RandomPictures_Source
	if content != "" { // match <name>
		for _, sourceEntry := range rpSources {
			if sourceEntry.GuildID == channel.GuildID {
				for _, alias := range sourceEntry.Aliases {
					if strings.ToLower(alias) == strings.ToLower(content) {
						matchEntry = sourceEntry
					}
				}
			}
		}
	} else { // match roles
		guildRoles, err := cache.GetSession().GuildRoles(channel.GuildID)
		if err != nil {
			if err, ok := err.(*discordgo.RESTError); ok && err.Message.Code == 50013 {
				guildRoles = []*discordgo.Role{}
			} else {
				return false, err
			}
		}
		targetMember, err := cache.GetSession().State.Member(channel.GuildID, msg.Author.ID)
		if err != nil {
			return false, err
		}
		slice.Sort(guildRoles, func(i, j int) bool {
			return guildRoles[i].Position > guildRoles[j].Position
		})
		for _, sourceEntry := range rpSources {
			if sourceEntry.GuildID == channel.GuildID {
				for _, alias := range sourceEntry.Aliases {
					if strings.ToLower(alias) == "@everyone" {
						matchEntry = sourceEntry
					}
				}
			}
		}
	CheckRoles:
		for _, guildRole := range guildRoles {
			for _, userRole := range targetMember.Roles {
				if guildRole.ID == userRole {
					for _, sourceEntry := range rpSources {
						if sourceEntry.GuildID == channel.GuildID {
							for _, alias := range sourceEntry.Aliases {
								if strings.Contains(strings.ToLower(guildRole.Name), strings.ToLower(alias)) {
									matchEntry = sourceEntry
									break CheckRoles
								}
							}
						}
					}
				}
			}
		}
	}
	if matchEntry.ID != "" {
		redisClient := cache.GetRedisClient()
		key := fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", matchEntry.ID, "count")
		pictureCount, err := redisClient.Get(key).Int64()
		if err == nil {
			chosenPicN := rand.Intn(int(pictureCount)) + 1
			key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-n:%s:entry:%d", matchEntry.ID, chosenPicN)
			fileHash := redisClient.Get(key).Val()
			if fileHash == "" {
				return false, errors.New("unable to gather data for pic")
			}
			key = fmt.Sprintf("robyul2-discord:randompictures:filescache:by-hash:%s", fileHash)
			resultBytes, err := redisClient.Get(key).Bytes()
			if err != nil {
				return false, errors.New("invalid picture data cached")
			}
			var gPicture *drive.File
			msgpack.Unmarshal(resultBytes, &gPicture)
			err = rp.postItem(channel.GuildID, msg.ChannelID, initialMessage.ID, gPicture, matchEntry.ID, strconv.Itoa(chosenPicN))
			if err == nil {
				return true, nil
			} else {
				return false, err
			}
		}
	}
	return false, errors.New("unable to match to source")
}

func (rp *RandomPictures) getFileCache(sourceEntry DB_RandomPictures_Source) []*drive.File {
	var allFiles []*drive.File

Loop:
	for {
		foldersChecked := make([]string, 0)
		foldersToCheck := sourceEntry.DriveFolderIDs
		for {
		CheckFoldersLoop:
			for _, driveFolderID := range foldersToCheck {
				for _, checkedFolder := range foldersChecked {
					if checkedFolder == driveFolderID {
						continue CheckFoldersLoop
					}
				}
				cache.GetLogger().WithField("module", "randompictures").Debug(fmt.Sprintf("getting google drive picture cache Folder #%s for Entry #%s", driveFolderID, sourceEntry.ID))
				result, err := driveService.Files.List().Q(fmt.Sprintf(driveSearchText, driveFolderID)).Fields(googleapi.Field(driveFieldsText)).PageSize(1000).Do()
				if err != nil {
					cache.GetLogger().WithField("module", "randompictures").Error(fmt.Sprintf("google drive error: %s, retrying in 10 seconds", err.Error()))
					time.Sleep(10 * time.Second)
					continue Loop
				}
				helpers.Relax(err)
				for _, file := range result.Files {
					if rp.isValidDriveFile(file) {
						allFiles = append(allFiles, file)
					}
					if file.MimeType == "application/vnd.google-apps.folder" {
						foldersToCheck = append(foldersToCheck, file.Id)
					}
				}

				for {
					if result.NextPageToken == "" {
						break
					}
					result, err = driveService.Files.List().Q(fmt.Sprintf(driveSearchText, driveFolderID)).Fields(googleapi.Field(driveFieldsText)).PageSize(1000).PageToken(result.NextPageToken).Do()
					helpers.Relax(err)
					for _, file := range result.Files {
						if rp.isValidDriveFile(file) {
							allFiles = append(allFiles, file)
						}
						if file.MimeType == "application/vnd.google-apps.folder" {
							foldersToCheck = append(foldersToCheck, file.Id)
						}
					}
				}
				foldersChecked = append(foldersChecked, driveFolderID)
			}
			if len(foldersChecked) == len(foldersToCheck) {
				break
			}
		}
		break
	}
	return allFiles
}

func (rp *RandomPictures) isValidDriveFile(file *drive.File) bool {
	if file.MimeType == "application/vnd.google-apps.folder" {
		return false
	}
	if file.Size > 8000000 { // bigger than 8 MB? (discords file size limit)
		return false
	}
	return true
}

func (rp *RandomPictures) updateImagesCachedMetric() {
	var totalImages int64

	var rpSources []DB_RandomPictures_Source
	cursor, err := rethink.Table("randompictures_sources").Run(helpers.GetDB())
	helpers.Relax(err)
	err = cursor.All(&rpSources)
	helpers.Relax(err)
	if err != nil && err != rethink.ErrEmptyResult {
		raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
		return
	}

	var key string
	var items int64
	redisClient := cache.GetRedisClient()
	for _, sourceEntry := range rpSources {
		key = fmt.Sprintf("robyul2-discord:randompictures:filescache:%s:entry:%s", sourceEntry.ID, "count")
		items, err = redisClient.Get(key).Int64()
		if err != nil {
			raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
			continue
		}
		totalImages += items
	}

	metrics.RandomPictureSourcesImagesCachedCount.Set(totalImages)
}

func (rp *RandomPictures) postItem(guildID string, channelID string, messageID string, file *drive.File, sourceID string, pictureID string) error {
	// fmt.Println("channelID:", channelID, "messageID:", messageID, "file:", file.Name, "sourceID:", sourceID, "pictureID:", pictureID)
	file, err := driveService.Files.Get(file.Id).Fields(googleapi.Field(driveFieldsSingleText)).Do()
	if err != nil {
		return err
	}

	camerModelText := ""
	if file.ImageMediaMetadata.CameraModel != "" {
		camerModelText = fmt.Sprintf(" 📷 `%s`", file.ImageMediaMetadata.CameraModel)
	}

	linkToPost := helpers.GetConfig().Path("imageproxy.base_url").Data().(string)

	splitFilename := strings.Split(file.Name, ".")

	linkToPost = fmt.Sprintf(linkToPost, rp.GetFileHash(sourceID, file.Id), url.QueryEscape(strings.Join(splitFilename[0:len(splitFilename)-1], "-")+"."+splitFilename[len(splitFilename)-1]))
	linkToHistory := helpers.GetConfig().Path("website.randompictures_base_url").Data().(string) + guildID

	// open link to prepare cache
	client := &http.Client{
		Timeout: 3 * time.Second,
	}
	request, err := http.NewRequest("GET", linkToPost, nil)
	if err == nil {
		request.Header.Set("User-Agent", helpers.DEFAULT_UA)
		_, err = client.Do(request)
		if err != nil {
			if errU, ok := err.(*url.Error); ok {
				if !strings.Contains(errU.Err.Error(), "Client.Timeout exceeded while awaiting headers") {
					raven.CaptureError(fmt.Errorf("%#v", errU.Err), map[string]string{})
				} else {
					cache.GetLogger().WithField("module", "randompictures").Warn(fmt.Sprintf("warming up cache for %s failed: time out", linkToPost))
				}
			} else {
				raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
			}
		}
	} else {
		raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
	}

	err = rp.appendLinkToServerHistory(linkToPost, sourceID, pictureID, file.Name, guildID)
	if err != nil {
		raven.CaptureError(fmt.Errorf("%#v", err), map[string]string{})
	}

	if messageID == "" {
		_, err = cache.GetSession().ChannelMessageSend(channelID, fmt.Sprintf(":label: `%s`%s\n%s", file.Name, camerModelText, linkToPost))
		if err != nil {
			return err
		}
	} else {
		_, err = cache.GetSession().ChannelMessageEdit(channelID, messageID, fmt.Sprintf(":label: `%s`%s\n%s\nCheck out a gallery of recent pictures here: <%s>", file.Name, camerModelText, linkToPost, linkToHistory))
		if err != nil {
			return err
		}
	}
	return nil
}

func (rp *RandomPictures) GetFileHash(sourceID string, fileID string) string {
	return helpers.GetMD5Hash(sourceID + "-" + fileID)
}

func (rp *RandomPictures) appendLinkToServerHistory(link string, sourceID string, pictureID string, fileName string, guildID string) error {
	redis := cache.GetRedisClient()
	key := fmt.Sprintf("robyul2-discord:randompictures:history:%s", guildID)

	item := new(RandomPictures_HistoryItem)
	item.Link = link
	item.SourceID = sourceID
	item.PictureID = pictureID
	item.Filename = fileName
	item.GuildID = guildID
	item.Time = time.Now()

	itemBytes, err := msgpack.Marshal(&item)
	if err != nil {
		return err
	}

	_, err = redis.LPush(key, itemBytes).Result()
	if err != nil {
		return err
	}

	_, err = redis.LTrim(key, 0, 99).Result()

	return err
}

type RandomPictures_HistoryItem struct {
	Link      string
	SourceID  string
	PictureID string
	Filename  string
	GuildID   string
	Time      time.Time
}

type ImgurResponse struct {
	Data    ImageData `json:"data"`
	Status  int       `json:"status"`
	Success bool      `json:"success"`
}

type ImageData struct {
	Account_id int    `json:"account_id"`
	Animated   bool   `json:"animated"`
	Bandwidth  int    `json:"bandwidth"`
	DateTime   int    `json:"datetime"`
	Deletehash string `json:"deletehash"`
	Favorite   bool   `json:"favorite"`
	Height     int    `json:"height"`
	Id         string `json:"id"`
	In_gallery bool   `json:"in_gallery"`
	Is_ad      bool   `json:"is_ad"`
	Link       string `json:"link"`
	Name       string `json:"name"`
	Size       int    `json:"size"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Views      int    `json:"views"`
	Width      int    `json:"width"`
	Error      string `json:"error"`
	Request    string `json:"request"`
	Method     string `json:"method"`
}
