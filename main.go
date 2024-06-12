package main

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/steelthedev/go-chat/handlers"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	// Connect to MongoDB
	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = client.Connect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(ctx)

	app := fiber.New()

	app.Get("/ping", func(ctx *fiber.Ctx) error {
		return ctx.SendString("Welcome to fiber")
	})

	appHandler := handlers.NewAppHandler()

	app.Get("/", appHandler.HandleGetIndex)

	server := NewWebSocket(client.Database("chatapp"))
	app.Get("/ws", websocket.New(func(ctx *websocket.Conn) {
		server.HandleWebSocket(ctx)
	}))

	go server.HandleMessages() 

	app.Listen(":3000")
}

func NewWebSocket(db *mongo.Database) *WebSocketServer {
	return &WebSocketServer{
		clients:   make(map[*websocket.Conn]string),
		broadcast: make(chan *Message),
		db:        db,
	}
}

type WebSocketServer struct {
	clients   map[*websocket.Conn]string
	broadcast chan *Message
	db        *mongo.Database
}

type Message struct {
	RoomID string `json:"roomId" bson:"roomId"`
	Text   string `json:"text" bson:"text"`
}

func (s *WebSocketServer) HandleWebSocket(ctx *websocket.Conn) {
	roomId := ctx.Query("roomId")
	if roomId == "" {
		log.Println("No roomId provided")
		ctx.Close()
		return
	}

	s.clients[ctx] = roomId
	defer func() {
		delete(s.clients, ctx)
		ctx.Close()
	}()

	s.sendChatHistory(ctx, roomId)

	for {
		_, msg, err := ctx.ReadMessage()
		if err != nil {
			log.Println("Read Error:", err)
			break
		}

		var message Message
		if err := json.Unmarshal(msg, &message); err != nil {
			log.Fatalf("Error Unmarshalling")
		}
		s.broadcast <- &message

		// Store the message in MongoDB
		collection := s.db.Collection("messages")
		_, err = collection.InsertOne(context.TODO(), message)
		if err != nil {
			log.Printf("MongoDB Insert Error: %v", err)
		}
	}
}

func (s *WebSocketServer) HandleMessages() {
	for {
		msg := <-s.broadcast

		for client, roomId := range s.clients {
			if roomId == msg.RoomID {
				err := client.WriteMessage(websocket.TextMessage, getMessageTemplate(msg))
				if err != nil {
					log.Printf("Write Error: %v", err)
					client.Close()
					delete(s.clients, client)
				}
			}
		}
	}
}

func getMessageTemplate(msg *Message) []byte {
	tmpl, err := template.ParseFiles("views/message.html")
	if err != nil {
		log.Fatalf("template parsing: %s", err)
	}

	var renderedMessage bytes.Buffer
	err = tmpl.Execute(&renderedMessage, msg)
	if err != nil {
		log.Fatalf("template execution: %s", err)
	}

	return renderedMessage.Bytes()
}

func (s *WebSocketServer) sendChatHistory(ctx *websocket.Conn, roomId string) {
	collection := s.db.Collection("messages")
	cur, err := collection.Find(context.TODO(), bson.M{"roomId": roomId})
	if err != nil {
		log.Printf("MongoDB Find Error: %v", err)
		return
	}
	defer cur.Close(context.TODO())

	for cur.Next(context.TODO()) {
		var msg Message
		err := cur.Decode(&msg)
		if err != nil {
			log.Printf("MongoDB Decode Error: %v", err)
			continue
		}
		err = ctx.WriteMessage(websocket.TextMessage, getMessageTemplate(&msg))
		if err != nil {
			log.Printf("WebSocket Write Error: %v", err)
			break
		}
	}

	if err := cur.Err(); err != nil {
		log.Printf("MongoDB Cursor Error: %v", err)
	}
}
