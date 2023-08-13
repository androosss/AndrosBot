package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/kkdai/youtube/v2"
	"gopkg.in/hraban/opus.v2"
)

type DownloadRequest struct {
	UserId    string
	ChannelId string
	YoutubeId string
	IsLoop    bool
}

type PlayRequest struct {
	UserId   string
	FilePath string
	Title    string
	IsLoop   bool
}

type SafeMap[Key comparable, Value any] struct {
	safeMap map[Key]Value
	sync.RWMutex
}

func (sm *SafeMap[Key, Value]) Get(k Key) Value {
	sm.RWMutex.RLock()
	defer sm.RWMutex.RUnlock()
	return sm.safeMap[k]
}

func (sm *SafeMap[Key, Value]) Save(k Key, v Value) {
	sm.RWMutex.Lock()
	defer sm.RWMutex.Unlock()
	sm.safeMap[k] = v
}

func (sm *SafeMap[Key, Value]) Init() {
	sm.safeMap = map[Key]Value{}
	sm.RWMutex = sync.RWMutex{}
}

var (
	discordToken = flag.String("discord.token", "", "Discord Token")
	youTubeToken = flag.String("youtube.token", "", "Youtube Token")

	downloadMap   SafeMap[string, chan DownloadRequest]
	playMap       SafeMap[string, chan PlayRequest]
	skipMap       SafeMap[string, chan string]
	disconnectMap SafeMap[string, chan string]
	isBusyMap     SafeMap[string, bool]
)

func init() {
	flag.Parse()
}

func main() {
	dg, err := discordgo.New("Bot " + *discordToken)
	if err != nil {
		fmt.Println("error creating Discord session,", err)
		return
	}
	downloadMap.Init()
	playMap.Init()
	skipMap.Init()
	disconnectMap.Init()
	isBusyMap.Init()
	dg.AddHandler(messageHandler)
	dg.AddHandler(banHandler)
	dg.AddHandler(voiceStateUpdateHandler)

	dg.Identify.Intents = discordgo.IntentsAll

	// Open a websocket connection to Discord and begin listening.
	err = dg.Open()
	if err != nil {
		fmt.Println("error opening connection,", err)
		return
	}

	err = dg.UpdateGameStatus(0, "🎶 Ja samo pevam 🎶")
	if err != nil {
		fmt.Println(err)
	}

	// Wait here until CTRL-C or other term signal is received.
	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Cleanly close down the Discord session.
	dg.Close()
}

const (
	youtubeApi   = "https://www.googleapis.com/youtube/v3/search"
	youtubeQuery = "?type=video&maxResults=1&key=%s&q="
)

func voiceStateUpdateHandler(s *discordgo.Session, m *discordgo.VoiceStateUpdate) {
	if m.ChannelID == "" && m.UserID == s.State.User.ID { // Bot disconnected from a voice channel
		disconnectMap.Get(m.GuildID) <- ""
	}
}

func messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	var err error
	defer func() {
		if err != nil {
			LogError(s, err.Error())
		}
	}()
	if m.Author.ID == s.State.User.ID {
		return
	}
	channel, err := s.Channel(m.ChannelID)
	if err != nil {
		return
	}
	if channel.GuildID == "719484888952209408" {
		if channel.Name == "general" {
			for i := range m.Attachments {
				if strings.HasPrefix(m.Attachments[i].ContentType, "image") {
					_, err = s.ChannelMessageSend(m.ChannelID, "BEZ MIMOVA NA GENERALU")
					if err != nil {
						return
					}
					_, err = s.ChannelMessageSend(m.ChannelID, "<:alobre:743938030624178287>")
					if err != nil {
						return
					}
					break
				}
			}
		}
		if channel.Name != "waifu-spam" && strings.HasPrefix(m.Content, "$") {
			_, err = s.ChannelMessageSend(m.ChannelID, "Imas waifu spam za to bre")
			if err != nil {
				return
			}
			_, err = s.ChannelMessageSend(m.ChannelID, "<:andros:807024966477676555>")
			if err != nil {
				return
			}
			err = s.ChannelMessageDelete(m.ChannelID, m.ID)
			if err != nil {
				return
			}
		}
		if channel.Name != "waifu-spam" && m.Author.Username == "Mudae" {
			err = s.ChannelMessageDelete(m.ChannelID, m.ID)
			if err != nil {
				return
			}
		}
		if m.Content == "<:andros:807024966477676555>" {
			_, err = s.ChannelMessageSend(m.ChannelID, "<:andros:807024966477676555>")
			if err != nil {
				return
			}
		}
	}
	if strings.HasPrefix(m.Content, "_") {
		if downloadMap.Get(m.GuildID) == nil {
			downloadMap.Save(m.GuildID, make(chan DownloadRequest))
			playMap.Save(m.GuildID, make(chan PlayRequest, 10))
			skipMap.Save(m.GuildID, make(chan string))
			disconnectMap.Save(m.GuildID, make(chan string))
			isBusyMap.Save(m.GuildID, false)
			go downloadAudio(s, m.GuildID)
			go playAudio(s, m.GuildID)
		}
		// music stuff
		switch m.Content[1] {
		case 'p':
			if len(m.Content) < 4 {
				LogError(s, "no prompt supplied")
				s.ChannelMessageSend(m.ChannelID, "Moras da imas nesto za trazenje <:andros:807024966477676555>")
				return
			}
			videoId, err := getVideoId(strings.SplitN(m.Content, " ", 2)[1])
			if err != nil {
				LogError(s, "can't find video id "+err.Error())
				return
			}
			downloadMap.Get(m.GuildID) <- DownloadRequest{
				YoutubeId: videoId,
				UserId:    m.Author.ID,
				ChannelId: m.ChannelID,
				IsLoop:    false,
			}
		case 's':
			skipMap.Get(m.GuildID) <- m.ChannelID
		case 'l':
			if len(m.Content) < 4 {
				LogError(s, "no prompt supplied")
				s.ChannelMessageSend(m.ChannelID, "Moras da imas nesto za trazenje <:andros:807024966477676555>")
				return
			}
			videoId, err := getVideoId(strings.SplitN(m.Content, " ", 2)[1])
			if err != nil {
				LogError(s, "can't find video id "+err.Error())
				return
			}
			downloadMap.Get(m.GuildID) <- DownloadRequest{
				YoutubeId: videoId,
				UserId:    m.Author.ID,
				ChannelId: m.ChannelID,
				IsLoop:    true,
			}
		case 'd':
			disconnectMap.Get(m.GuildID) <- m.ChannelID
		}
	}
}

func banHandler(s *discordgo.Session, b *discordgo.GuildBanAdd) {
	var err error
	defer func() {
		if err != nil {
			LogError(s, err.Error())
		}
	}()
	if b.User.ID != "258578799917006849" {
		return
	}
	err = s.GuildBanDelete(b.GuildID, b.User.ID)
	if err != nil {
		return
	}
	channel, err := s.UserChannelCreate(b.User.ID)
	if err != nil {
		return
	}
	_, err = s.ChannelMessageSend(channel.ID, "discord.gg/t6KQszd")
}

func downloadAudio(s *discordgo.Session, guildId string) {
	fileNr := 0
	for {
		select {
		case req := <-downloadMap.Get(guildId):
			if len(playMap.Get(guildId)) == cap(playMap.Get(guildId)) {
				LogError(s, "queue is full")
				continue
			}
			client := youtube.Client{}

			video, err := client.GetVideo(req.YoutubeId)
			if err != nil {
				LogError(s, "couldn't get video "+err.Error())
				s.ChannelMessageSend(req.ChannelId, "Ae iskopiraj link normalno <:andros:807024966477676555>")
				continue
			}

			formats := video.Formats.WithAudioChannels() // only get videos with audio
			sort.Slice(formats, func(i int, j int) bool {
				return audioQualVal[formats[i].AudioQuality] > audioQualVal[formats[j].AudioQuality] ||
					(audioQualVal[formats[i].AudioQuality] == audioQualVal[formats[j].AudioQuality] &&
						qualVal[formats[i].Quality] > qualVal[formats[j].Quality])
			}) // take video with best audio quality and least amount of memory
			stream, _, err := client.GetStream(video, &formats[0])
			if err != nil {
				LogError(s, "couldn't get stream "+err.Error())
			}

			format := strings.Split(strings.Split(formats[0].MimeType, ";")[0], "/")[1]

			name := `./assets/guild_` + guildId + `/audio` + fmt.Sprint(fileNr) + "." + format
			output := `./assets/guild_` + guildId + `/audio` + fmt.Sprint(fileNr) + ".opus"
			fileNr = (fileNr + 1) % 10

			err = os.MkdirAll(path.Dir(name), 0770)
			if err != nil {
				LogError(s, "couldn't create dirs "+err.Error())
				continue
			}
			file, err := os.Create(name)
			if err != nil {
				LogError(s, "couldn't create file "+err.Error())
				continue
			}

			_, err = io.Copy(file, stream)
			if err != nil {
				LogError(s, "couldn't copy file "+err.Error())
				file.Close()
				continue
			}
			file.Close()
			cmd := exec.Command("ffmpeg", "-y", "-i", name, "-f", "s16le", "-ar", "48000", "-ac", "2", output)
			if out, err := cmd.CombinedOutput(); err != nil {
				LogError(s, "couldn't turn into .opus "+err.Error()+" output: "+string(out))
				continue
			}

			sort.Slice(video.Thumbnails, func(i, j int) bool {
				return video.Thumbnails[i].Width > video.Thumbnails[j].Width
			})
			if !isBusyMap.Get(guildId) {
				_, err := s.ChannelMessageSendComplex(req.ChannelId, &discordgo.MessageSend{
					Content: "Pušta se " + video.Title,
					Embeds: []*discordgo.MessageEmbed{
						&discordgo.MessageEmbed{
							URL:  video.Thumbnails[0].URL,
							Type: discordgo.EmbedTypeImage,
							Image: &discordgo.MessageEmbedImage{
								URL:    video.Thumbnails[0].URL,
								Height: int(video.Thumbnails[0].Height),
								Width:  int(video.Thumbnails[0].Width),
							},
						},
					},
				})
				if err != nil {
					LogError(s, "can't send message "+err.Error())
				}
			} else {
				_, err := s.ChannelMessageSendComplex(req.ChannelId, &discordgo.MessageSend{
					Content: video.Title + " je dodat u queue",
					Embeds: []*discordgo.MessageEmbed{
						&discordgo.MessageEmbed{
							URL:  video.Thumbnails[0].URL,
							Type: discordgo.EmbedTypeImage,
							Image: &discordgo.MessageEmbedImage{
								URL:    video.Thumbnails[0].URL,
								Height: int(video.Thumbnails[0].Height),
								Width:  int(video.Thumbnails[0].Width),
							},
						},
					},
				})
				if err != nil {
					LogError(s, "can't send message "+err.Error())
				}
			}
			playMap.Get(guildId) <- PlayRequest{
				FilePath: output,
				UserId:   req.UserId,
				IsLoop:   req.IsLoop,
				Title:    video.Title,
			}
		}
	}
}

const (
	channels  int = 2                   // 1 for mono, 2 for stereo
	frameRate int = 48000               // audio sampling rate
	frameSize int = 960                 // uint16 size of each audio frame
	maxBytes  int = (frameSize * 2) * 2 // max size of opus data
)

func playAudio(s *discordgo.Session, guildId string) {
outer:
	for {
		select {
		case req := <-playMap.Get(guildId):
		empty:
			for {
				select {
				case <-skipMap.Get(guildId):
				case <-disconnectMap.Get(guildId):
				default:
					break empty
				}
			}
			isBusyMap.Save(guildId, true)
			buffer := [][]int16{}
			audio, err := os.Open(req.FilePath)
			if err != nil {
				LogError(s, "can't open file "+err.Error())
				continue
			}

			for {

				// Read encoded pcm from dca file.
				audioBuf := make([]int16, frameSize*channels)
				err = binary.Read(audio, binary.LittleEndian, &audioBuf)

				if err == io.EOF || err == io.ErrUnexpectedEOF {
					err := audio.Close()
					if err != nil {
						LogError(s, "can't close file "+err.Error())
						continue outer
					}
					break
				}

				// Should not be any end of file errors
				if err != nil {
					LogError(s, "Error reading from opus file :"+err.Error())
					continue outer
				}

				// Append encoded pcm data to the buffer.
				buffer = append(buffer, audioBuf)
			}

			activeVC, err := connectToUserChannel(s, guildId, req.UserId)
			if err != nil {
				LogError(s, "can't connect to voice "+err.Error())
				continue
			}

			// Start speaking.
			activeVC.Speaking(true)

			opusEncoder, err := opus.NewEncoder(frameRate, channels, opus.AppAudio)
			// Send the buffer data.
		loop:
			for {
				for _, buff := range buffer {
					select {
					case channelId := <-skipMap.Get(guildId):
						s.ChannelMessageSend(channelId, req.Title+" je preskočen")
						break loop
					case channelId := <-disconnectMap.Get(guildId):
						disconnectInGuild(s, guildId)
					diconnectEmpty:
						for {
							select {
							case <-playMap.Get(guildId):
							default:
								break diconnectEmpty
							}
						}
						if channelId != "" {
							s.ChannelMessageSend(channelId, "Diskonektovanje")
						}
						continue outer
					default:
						dataToSend := make([]byte, frameSize*channels*2)
						bytesWritten, err := opusEncoder.Encode(buff, dataToSend)
						if err != nil {
							LogError(s, "can't encode data "+err.Error())
							break
						}
						activeVC.OpusSend <- dataToSend[:bytesWritten]
					}
				}
				if !req.IsLoop {
					break
				}
			}

			//Stop speaking
			activeVC.Speaking(false)
			isBusyMap.Save(guildId, false)
			if len(playMap.Get(guildId)) == 0 {
				disconnectInGuild(s, guildId)
			}
			files, err := filepath.Glob(strings.TrimSuffix(req.FilePath, ".opus") + `\.*`)
			if err != nil {
				LogError(s, "can't find files to delete "+err.Error())
				break
			}
			for _, f := range files {
				if err := os.Remove(f); err != nil {
					LogError(s, "can't delete files "+err.Error())
				}
			}
		}
	}
}

var qualVal = map[string]int{
	"hd1080": 1,
	"hd720":  2,
	"720p":   3,
	"large":  4,
	"medium": 5,
	"small":  6,
	"tiny":   7,
}

var audioQualVal = map[string]int{
	"AUDIO_QUALITY_HIGH":   3,
	"AUDIO_QUALITY_MEDIUM": 2,
	"AUDIO_QUALITY_LOW":    1,
}

func connectToUserChannel(s *discordgo.Session, guildId, userId string) (*discordgo.VoiceConnection, error) {

	// Find the guild for that channel.
	g, err := s.State.Guild(guildId)
	if err != nil {
		return nil, err
	}

	// Look for the message sender in that guild's current voice states.
	for _, vs := range g.VoiceStates {
		if vs.UserID == userId {
			return s.ChannelVoiceJoin(guildId, vs.ChannelID, false, true)
		}
	}
	return nil, fmt.Errorf("user isn't in voice")
}

func disconnectInGuild(s *discordgo.Session, guildId string) {
	vc := s.VoiceConnections[guildId]
	if vc != nil {
		err := vc.Disconnect()
		if err != nil {
			LogError(s, "error disconnecting "+err.Error())
		}
	}
}

func getVideoId(content string) (videoId string, err error) {
	if _, id, found := strings.Cut(content, "youtube.com/watch?v="); found {
		videoId = id
	} else {
		var req *http.Request
		var resp *http.Response
		q := url.QueryEscape(content)
		req, err = http.NewRequest(http.MethodGet, youtubeApi+fmt.Sprintf(youtubeQuery, *youTubeToken)+q, nil)
		if err != nil {
			return
		}
		client := http.Client{}
		resp, err = client.Do(req)
		if err != nil {
			return
		}
		var respMap map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&respMap)
		if err != nil {
			return
		}
		itemsArr, ok := respMap["items"].([]interface{})
		if !ok || len(itemsArr) == 0 {
			err = fmt.Errorf("no video found")
			return
		}
		items, ok := itemsArr[0].(map[string]interface{})
		if !ok {
			err = fmt.Errorf("no video found")
			return
		}
		id, ok := items["id"].(map[string]interface{})
		if !ok {
			err = fmt.Errorf("no video found")
			return
		}
		videoId, ok = id["videoId"].(string)
		if !ok {
			err = fmt.Errorf("no video found")
		}
	}
	return
}

func isConnected(s *discordgo.Session, guildId string) bool {
	g, err := s.State.Guild(guildId)
	if err != nil {
		return false
	}

	// Look for the message sender in that guild's current voice states.
	for _, vs := range g.VoiceStates {
		if vs.UserID == s.State.User.ID {
			return true
		}
	}

	return false
}

func LogError(s *discordgo.Session, message string) {
	fmt.Println(message)
	ch, err := s.UserChannelCreate("258578799917006849")
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	_, err = s.ChannelMessageSend(ch.ID, message)
	if err != nil {
		fmt.Println(err.Error())
	}
}
