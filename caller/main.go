package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/joho/godotenv"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"golang.org/x/oauth2"
)

func getOpenAIAPI() *http.Client {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("OPENAI_API_KEY")},
	)
	return oauth2.NewClient(ctx, ts)
}

var lastPrompt string

func chatGPTResponse(client *http.Client, prompt string) (string, error) {
	// 前回の応答と同じプロンプトの場合、応答を返さない
	if prompt == lastPrompt {
		return "", nil
	}

	lastPrompt = prompt

	messages := []map[string]string{
		{
			"role":    "user",
			"content": prompt,
		},
	}

	for _, message := range messages {
		message["content"] = html.EscapeString(message["content"])
	}

	data := map[string]interface{}{
		"model":      "gpt-3.5-turbo",
		"messages":   messages,
		"max_tokens": 100,
	}

	requestData, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	fmt.Printf("Sending request data: %s\n", string(requestData))

	resp, err := client.Post("https://api.openai.com/v1/chat/completions", "application/json", bytes.NewBuffer(requestData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("error: HTTP %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]interface{}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	choices := result["choices"].([]interface{})
	if len(choices) == 0 {
		return "すみません、現在お手伝いできません。", nil
	}

	firstChoice := choices[0].(map[string]interface{})
	message := firstChoice["message"].(map[string]interface{})
	responseText := message["content"].(string)

	return strings.TrimSpace(responseText), nil
}

// handleAppMention関数を変更して、botUserIDを引数として受け取る
func handleAppMention(api *slack.Client, client *http.Client, botUserID string, event *slackevents.MessageEvent) {
	if event.User == botUserID {
		return
	}

	// メンションの場合のみプロンプトを設定
	prompt := ""
	if strings.HasPrefix(event.Text, "<@"+botUserID+">") {
		prompt = strings.TrimSpace(strings.TrimPrefix(event.Text, "<@"+botUserID+">"))
	} else {
		// DMの場合、テキスト全体をプロンプトとして使用
		prompt = event.Text
	}

	if prompt != "" {
		fmt.Printf("Prompt received: %s\n", prompt)
		chatGPT, err := chatGPTResponse(client, prompt)
		if err != nil {
			fmt.Printf("Error getting response from ChatGPT: %v\n", err)
		} else {
			fmt.Printf("Response from ChatGPT: %s\n", chatGPT)
			_, _, err := api.PostMessage(event.Channel, slack.MsgOptionText(chatGPT, false))
			if err != nil {
				fmt.Printf("Error posting message to Slack: %v\n", err)
			}
		}
	}
}

func HandleURLVerification(body string) (*slackevents.ChallengeResponse, error) {
	var res *slackevents.ChallengeResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		return nil, err
	}

	return res, nil
}

func handler(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	api := slack.New(os.Getenv("SLACK_BOT_TOKEN"))
	client := getOpenAIAPI()

	// 自分のボットのユーザーIDを取得する
	info, err := api.AuthTest()
	if err != nil {
		log.Fatalf("Error getting bot user ID: %v", err)
	}
	botUserID := info.UserID

	// new line
	body := request.Body
	fmt.Printf("HERE: %s\n", body)
	eventsAPIEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		return events.APIGatewayProxyResponse{Body: "slack conection error", StatusCode: 500}, err
	}

	switch eventsAPIEvent.Type {
	case slackevents.URLVerification:
		res, err := HandleURLVerification(body)
		if err != nil {
			return events.APIGatewayProxyResponse{Body: "slack conection error", StatusCode: 500}, err
		}
		return events.APIGatewayProxyResponse{
			Body:       res.Challenge,
			StatusCode: 200,
		}, nil

	case slackevents.CallbackEvent:
		innerEvent := eventsAPIEvent.InnerEvent
		fmt.Printf("Inner event type: %T\n", innerEvent.Data)
		switch ev := innerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			fmt.Println("AppMention event received")
			if ev.User != botUserID {
				defer handleAppMention(api, client, botUserID, &slackevents.MessageEvent{
					User:    ev.User,
					Channel: ev.Channel,
					Text:    ev.Text,
				})
			}
			return events.APIGatewayProxyResponse{
				Body:       "",
				StatusCode: 200,
			}, nil
		case *slackevents.MessageEvent:
			fmt.Println("Message event received")
			if ev.User != botUserID && ev.ChannelType == "im" {
				handleAppMention(api, client, botUserID, ev)
			}
		default:
			fmt.Printf("Unknown event type: %T\n", ev)
		}
	default:
		fmt.Printf("Unknown event type: %s\n", eventsAPIEvent.Type)
	}

	return events.APIGatewayProxyResponse{
		Body:       "",
		StatusCode: 200,
	}, nil
}

func main() {
	lambda.Start(handler)
}
