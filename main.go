package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
	"github.com/urfave/cli/v2"
	"golang.org/x/net/html"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

type State struct {
	Channel   *youtube.Channel `json:"channel"`
	LiveVideo *youtube.Video   `json:"liveVideo"`
	Videos    []*youtube.Video `json:"videos"`
}

var (
	re    = regexp.MustCompile(`(?i)https://www\.youtube\.com/watch\?v=(.+)`)
	state = new(State)
)

func startWebServer(port int) error {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().
			Set("content-type", "application/json")

		json.NewEncoder(w).
			Encode(state)
	})

	return http.ListenAndServe(fmt.Sprintf(":%d", port), handler)
}

func fetchLiveVideoId(channelId string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://www.youtube.com/channel/%s/live", channelId), nil)

	if err != nil {
		return "", err
	}

	req.AddCookie(&http.Cookie{
		Name:   "CONSENT",
		Value:  "YES+42",
		Secure: true,
	})

	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		return "", err
	}

	doc, err := html.Parse(resp.Body)

	if err != nil {
		return "", err
	}

	sel, err := cascadia.Parse("link[rel='canonical']")

	if err != nil {
		return "", err
	}

	if node := cascadia.Query(doc, sel); node != nil {
		for _, v := range node.Attr {
			if v.Key == "href" {
				if sm := re.FindStringSubmatch(v.Val); len(sm) > 0 {
					return sm[1], nil
				}
			}
		}
	}

	return "", nil
}

func fetchChannel(src *youtube.Service, channelId string) (*youtube.Channel, error) {
	resp, err := src.Channels.List([]string{"contentDetails", "snippet", "statistics"}).
		Id(channelId).
		Do()

	if err != nil {
		return nil, err
	}

	if len(resp.Items) > 0 {
		return resp.Items[0], nil
	}

	return nil, nil
}

func fetchPlaylistItems(src *youtube.Service, playlistId string) ([]*youtube.PlaylistItem, error) {
	resp, err := src.PlaylistItems.List([]string{"contentDetails", "snippet"}).
		PlaylistId(playlistId).
		MaxResults(25).
		Do()

	if err != nil {
		return nil, err
	}

	return resp.Items, nil
}

func fetchVideos(src *youtube.Service, videoIds []string) ([]*youtube.Video, error) {
	resp, err := src.Videos.List([]string{"snippet", "statistics", "liveStreamingDetails"}).
		Id(videoIds...).
		Do()

	if err != nil {
		return nil, err
	}

	return resp.Items, nil
}

func refresh(src *youtube.Service, channelId string) error {
	channel, err := fetchChannel(src, channelId)

	if err != nil {
		return err
	}

	liveVideoId, err := fetchLiveVideoId(channel.Id)

	if err != nil {
		return err
	}

	playlistItems, err := fetchPlaylistItems(src, channel.ContentDetails.RelatedPlaylists.Uploads)

	if err != nil {
		return err
	}

	videoIds := make([]string, 0)

	if liveVideoId != "" {
		videoIds = append(videoIds, liveVideoId)
	}

	for _, v := range playlistItems {
		videoIds = append(videoIds, v.ContentDetails.VideoId)
	}

	state.Channel = channel

	if len(videoIds) == 0 {
		state.Videos = make([]*youtube.Video, 0)
		state.LiveVideo = nil

		return nil
	}

	videos, err := fetchVideos(src, videoIds)

	if err != nil {
		return err
	}

	var liveVideo *youtube.Video

	if len(videos) > 0 && videos[0].Id == liveVideoId {
		liveVideo = videos[0]
		videos = videos[1:]
	}

	state.LiveVideo = liveVideo
	state.Videos = videos

	return nil
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack

	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out: os.Stdout,
	})

	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "key",
				Aliases:  []string{"k"},
				EnvVars:  []string{"API_KEY"},
				Usage:    "The YouTube API key",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "channel",
				Aliases:  []string{"c"},
				EnvVars:  []string{"CHANNEL_ID"},
				Usage:    "The YouTube channel ID",
				Required: true,
			},
			&cli.IntFlag{
				Name:    "port",
				Aliases: []string{"p"},
				EnvVars: []string{"PORT"},
				Usage:   "The server port to use",
				Value:   3000,
			},
		},
		Action: func(ctx *cli.Context) error {
			key := ctx.String("key")
			channel := ctx.String("channel")
			port := ctx.Int("port")

			src, err := youtube.NewService(context.Background(), option.WithAPIKey(key))

			if err != nil {
				log.Fatal().Err(err).Msg("Unable to initialize YouTube service")
			}

			go startWebServer(port)

			for {
				if err := refresh(src, channel); err != nil {
					log.Err(err).Msgf("Unable to refresh state")
				}

				time.Sleep(time.Minute)
			}
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal().Err(err)
	}
}
