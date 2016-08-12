package MicroServiceLib

//
// Copyright (C) Philip Schlump, 2015-2016.
//

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	newUuid "github.com/pborman/uuid"
	"github.com/pschlump/Go-FTL/server/lib"
	"github.com/pschlump/Go-FTL/server/sizlib"
	"github.com/pschlump/MiscLib"
	"github.com/pschlump/godebug"
	"github.com/pschlump/radix.v2/pool" // Modified pool to have NewAuth for authorized connections
	"github.com/pschlump/radix.v2/pubsub"
	"github.com/pschlump/radix.v2/redis"
	"github.com/pschlump/uuid"
	"github.com/taskcluster/slugid-go/slugid"
)

// "www.2c-why.com/h2ppp/lib/H2pppCommon"
// "github.com/pschlump/Go-FTL/server/sizlib"

//
// xyzzy - q size, notify on q larger than X or q wait longer than x
// xyzzy - q action - if q larger than X then ... -- start server --
//

//
// Bot - the micro service - lisents, works, replys
// Cli - the command line test for this, start, actions(interpreted)
//	$ Cli/Cli -h 192.168.0.133 -p RedisPort -a RedisPass -c cfgFile.json -i input -o output
//	>>> s Message1 Take-Sec				# send a message to Bot taking Sec of time
//  >>> lr								# List Reply Fx data
//	>>> s Message2 Take-Sec				# send a message to Bot taking Sec of time
//  >>> lr								# List Reply Fx data
//  >>>
//  *** MessageReply: Message1
//  >>>
//  *** MessageReply: Message2
//  >>>
//  >>>
//  >>> q                               # quit
//

type MsMessageParams struct {
	Name  string
	Value string
}

type MsMessageToSend struct {
	Id            string            `json:"Id"`                 // Unique id UUID for this message
	ActionId      string            `json:"ActionId,omitempty"` // Activity Hash ID -- unique to this activity
	GroupId       string            `json:"GroupId,omitempty"`  // Group of id's together ID -- unique to group
	CallId        string            `json:"CallId,omitempty"`   // Unique id for this call - if empty - generated and return
	SendBackTo    string            `json:"SendBackTo"`         // -- send message to -- WakieWakie
	ReplyTTL      uint64            `json:"ReplyTTL"`           // how long will a reply last if not picked up.
	To            string            `json:"-"`                  // Desitnation work Q - a name -
	ClientTimeout uint64            `json:"-"`                  // How long are you giving client to perform task
	Params        []MsMessageParams `json:"Params"`             // Set of params for client
	IsTimeout     bool              `json:"-"`                  // set on reply
}

type ReplyFxType struct {
	Key        string                                                        // translated ID for reply
	IsTimedOut bool                                                          // Flag indicating timeout occured and fx called with that.
	IsDead     bool                                                          // Flag indicating timeout occured and fx called with that.
	TickCount  int                                                           // Number of time ticks encounted
	TickMax    int                                                           // Max before timeout is set
	Fx         func(data *MsMessageToSend, rx *ReplyFxType, isTimedOut bool) //function that will get called when Key is found
}

// Redis Connect Info ( 2 channels )
type MsCfgType struct {
	ServerId            string                              `json:"Sid"`              //
	Name                string                              `json:"QName"`            //	// Name of the Q to send stuff to //
	ReplyTTL            uint64                              `json:"ReplyTTL"`         // how long will a reply last if not picked up.
	isRedisConnected    bool                                `json:"-"`                // If connect to redis has occured for wakie-wakie calls
	RedisConnectHost    string                              `json:"RedisConnectHost"` // Connection infor for Redis Database
	RedisConnectPort    string                              `json:"RedisConnectPort"` //
	RedisConnectAuth    string                              `json:"RedisConnectAuth"` //
	ReplyListenQ        string                              `json:"ReplyListenQ"`     // Receives Wakie Wakie on Return
	RedisPool           *pool.Pool                          `json:"-"`                // Pooled Redis Client connectioninformation
	Err                 error                               `json:"-"`                // Error Holding Pen
	subClient           *pubsub.SubClient                   `json:"-"`                //
	subChan             chan *pubsub.SubResp                `json:"-"`                //
	timeout             chan bool                           `json:"-"`                //
	ReplyFx             map[string]*ReplyFxType             `json:"-"`                //	Set of call/respond that we are waiting for answers on.
	DebugTimeoutMessage bool                                `json:"-"`                // turn on/off the timeout 1ce a second message
	TickInMilliseconds  int                                 `json:"-"`                // # of miliseconds for 1 tick
	ReplyFunc           func(replyMessage *MsMessageToSend) // function that will get when reply occures
}

//func init() {
//}

type WorkFuncType func(arb map[string]interface{})

func NewMsCfgType(qName, qReply string) (ms *MsCfgType) {
	var err error
	// ms.ReplyListenQ = "cli-test1-reply"                   // xyzzy-from-config -- template --
	qr := "q-r:%{ServerId%}" // This is the lisened to by client for wakie-wakie on client side
	if qReply != "" {
		qr = qReply
	}
	ms = &MsCfgType{
		ServerId:           UUIDAsStrPacked(),             //
		Err:                err,                           //
		Name:               qName,                         // Name of message Q that will be published on
		ReplyListenQ:       qr,                            // This is the lisened to by client for wakie-wakie on client side
		TickInMilliseconds: 100,                           // 100 milliseconds
		ReplyFx:            make(map[string]*ReplyFxType), //
	}
	return
}

// setRedisPool - sets the redis poo info in the MsCfgType
//
// Example:
// 		micro := NewMsCfgType()
// 		micro.SetRedisPool(hdlr.gCfg.RedisPool)
func (ms *MsCfgType) SetRedisPool(pool *pool.Pool) {
	ms.RedisPool = pool
}

func (ms *MsCfgType) SetRedisConnectInfo(h, p, a string) {
	ms.RedisConnectHost = h
	ms.RedisConnectPort = p
	ms.RedisConnectAuth = a
}

// GetRedis gets the redis connection
func (ms *MsCfgType) GetRedis() (conn *redis.Client) {
	//if ms.conn != nil {
	//	conn = ms.conn
	//	return
	//}
	var err error
	conn, err = ms.RedisPool.Get()
	ms.Err = err
	//ms.conn = conn
	return
}

// PutRedis release the connection back to the connection pool
func (ms *MsCfgType) PutRedis(conn *redis.Client) {
	ms.RedisPool.Put(conn)
	// 	ms.conn = nil
}

//func (ms *MsCfgType) CheckForCompletedActivity(ActionId string) (message MsMessageToSend, found bool) {
//	// Check in redis for a completed data on this, if so return previous data and found true
//	conn := ms.GetRedis()
//	defer ms.PutRedis()
//
//	key := "q-r:" + ActionId // this key will timeout and disapear causing a re-generation of work after it's TTL
//
//	v, err := conn.Cmd("GET", key).Str()
//	if err != nil {
//		if db4 {
//			fmt.Fprintf(os.Stderr, "%sInfo on redis - did not find - get(%s): %s, it's ok, %s%s\n", MiscLib.ColorGreen, key, err, godebug.LF(), MiscLib.ColorReset)
//		}
//		return
//	}
//
//	err = json.Unmarshal([]byte(v), &message)
//	if err != nil {
//		fmt.Printf("%sError on redis/unmarshal - (%s)/(%s): Error:%s, %s%s\n", MiscLib.ColorRed, key, v, err, godebug.LF(), MiscLib.ColorReset)
//		return
//	}
//
//	found = true
//	return
//}

func (ms *MsCfgType) SendMessage(mm *MsMessageToSend) {
	if mm.CallId == "" {
		mm.CallId = UUIDAsStrPacked()
	}

	mm.ReplyTTL = ms.ReplyTTL
	mdata := make(map[string]string)
	mdata["ServerId"] = ms.ServerId
	mm.SendBackTo = sizlib.Qt(ms.ReplyListenQ, mdata)

	var wg sync.WaitGroup

	createReplyFunc := func() func(replyMessage *MsMessageToSend, rx *ReplyFxType, isTimeOut bool) {
		fx := ms.ReplyFunc
		origMsg := *mm
		if fx != nil {
			wg.Add(1)
		}
		return func(replyMessage *MsMessageToSend, rx *ReplyFxType, isTimeOut bool) {
			if isTimeOut {
				fmt.Fprintf(os.Stderr, "%sFx Called! Failed to get a reply to %s in a timely fashion, %s%s\n", MiscLib.ColorYellow, godebug.SVar(origMsg), godebug.LF(), MiscLib.ColorReset)
				if fx != nil {
					origMsg.IsTimeout = true
					fx(&origMsg) // function that will get when reply occures
					wg.Done()
				}
			} else {
				fmt.Fprintf(os.Stderr, "%sFx Called! Reply to orig=%s is reply=-->>%s<<--, %s%s\n", MiscLib.ColorGreen, godebug.SVar(origMsg), godebug.SVar(replyMessage), godebug.LF(), MiscLib.ColorReset)
				if fx != nil {
					replyMessage.IsTimeout = false
					fx(replyMessage) // function that will get when reply occures
					wg.Done()
				}
			}
		}
	}

	ms.ReplyFx[mm.CallId] = &ReplyFxType{
		Key:       mm.CallId,
		TickCount: 0,
		TickMax:   20,
		Fx:        createReplyFunc(),
	}

	// 1. Put mesage on work Q
	key := "w-q:" + ms.Name
	theMessage := StructToJson(mm)
	conn := ms.GetRedis()
	defer ms.PutRedis(conn)
	err := conn.Cmd("RPUSH", key, theMessage).Err
	if err != nil {
		// logrus.Infof(`{"msg":"Error %s Unable to SADD to trx|list in redis.","LineFile":%q}`+"\n", err, godebug.LF())
		fmt.Printf("%sError on add work to q - (%s)/(%s): Error:%s, %s%s\n", MiscLib.ColorRed, key, theMessage, err, godebug.LF(), MiscLib.ColorReset)
		return
	}
	fmt.Fprintf(os.Stderr, `%sRPUSH "%s" "%s"`+"%s\n", MiscLib.ColorGreen, key, theMessage, MiscLib.ColorReset)

	// 2. Do a wakie-wakie pub/sub
	key = ms.Name
	pmsg := `{"cmd":"wakie-wakie"}`
	fmt.Fprintf(os.Stderr, `%sPUBLISH "%s" "%s"`+"%s\n", MiscLib.ColorGreen, key, pmsg, MiscLib.ColorReset)
	err = conn.Cmd("PUBLISH", key, pmsg).Err
	// xyzzy00002 - do somethig with err

	wg.Wait()

	return
}

// ReceiveReply is the callers way of getting back a response to a message or set of messages. This is a CallResponse by CallId reply.
// You also get a call to the worker fucntion when the timeout (if specified) occures.
//
// The replay pub/sub is w-r:Name
//func (ms *MsCfgType) ReceiveCallReply() (message MsMessageToSend, found bool) {
//
//	// xyzzy - wait for timeout or wakie-wakie
//
//	conn := ms.GetRedis()
//	defer ms.PutRedis()
//
//	// xyzzy - this should be a function that we put into ms. in the reply table.  ms.ReplyFx           map[string]ReplyFxType `json:"-"`                 //
//
//	key := "xyzzy" // create data and use qt to template the key to get
//
//	v, err := conn.Cmd("GET", key).Str()
//	if err != nil {
//		if db4 {
//			fmt.Fprintf(os.Stderr, "%sInfo on redis - did not get reply to call - get(%s): %s, it's ok, %s%s\n", MiscLib.ColorRed, key, err, godebug.LF(), MiscLib.ColorReset)
//		}
//		// xyzzy - should act like timeout
//		return
//	}
//
//	err = json.Unmarshal([]byte(v), &message)
//	if err != nil {
//		fmt.Printf("%sError on redis/unmarshal - (%s)/(%s): Error:%s, %s%s\n", MiscLib.ColorRed, key, v, err, godebug.LF(), MiscLib.ColorReset)
//		// xyzzy - should act like timeout
//		return
//	}
//
//	found = true
//	return
//
//}

func (ms *MsCfgType) SetupListen() {

	client, err := redis.Dial("tcp", ms.RedisConnectHost+":"+ms.RedisConnectPort)
	if err != nil {
		log.Fatal(err)
	}
	if ms.RedisConnectAuth != "" {
		err = client.Cmd("AUTH", ms.RedisConnectAuth).Err
		if err != nil {
			log.Fatal(err)
		} else {
			fmt.Fprintf(os.Stderr, "Success: Connected to redis-server with AUTH.\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "Success: Connected to redis-server.\n")
	}

	ms.subClient = pubsub.NewSubClient(client) // subClient *pubsub.SubClient
	ms.subChan = make(chan *pubsub.SubResp)
	ms.timeout = make(chan bool, 1)

	mdata := make(map[string]string)
	mdata["ServerId"] = ms.ServerId
	SendBackTo := sizlib.Qt(ms.ReplyListenQ, mdata)
	fmt.Printf("%sListening for replys on: %s, %s\n", MiscLib.ColorCyan, SendBackTo, MiscLib.ColorReset)
	sr := ms.subClient.Subscribe(SendBackTo)
	if sr.Err != nil {
		fmt.Fprintf(os.Stderr, "%sError: subscribe, %s.%s\n", MiscLib.ColorRed, sr.Err, MiscLib.ColorReset)
	}
}

func (ms *MsCfgType) SetupListenServer() {

	client, err := redis.Dial("tcp", ms.RedisConnectHost+":"+ms.RedisConnectPort)
	if err != nil {
		log.Fatal(err)
	}
	if ms.RedisConnectAuth != "" {
		err = client.Cmd("AUTH", ms.RedisConnectAuth).Err
		if err != nil {
			log.Fatal(err)
		} else {
			fmt.Fprintf(os.Stderr, "Success: Connected to redis-server with AUTH.\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "Success: Connected to redis-server.\n")
	}

	ms.subClient = pubsub.NewSubClient(client) // subClient *pubsub.SubClient
	ms.subChan = make(chan *pubsub.SubResp)
	ms.timeout = make(chan bool, 1)

	fmt.Printf("%sServer: Listening for work on: %s, %s\n", MiscLib.ColorCyan, ms.Name, MiscLib.ColorReset)
	sr := ms.subClient.Subscribe(ms.Name)

	if sr.Err != nil {
		fmt.Fprintf(os.Stderr, "%sError: subscribe, %s.%s\n", MiscLib.ColorRed, sr.Err, MiscLib.ColorReset)
	}
}

// ms.TimeOutMessage(flag)
func (ms *MsCfgType) TimeOutMessage(f bool) {
	ms.DebugTimeoutMessage = f
}

func (ms *MsCfgType) ListenFor() { // server *socketio.Server) {

	// ms needs a hash of IDs for calls, and fx to call (closure??) for each call when ID is returned.
	//  ms.ReplyFx           map[string]ReplyFxType `json:"-"`                 //

	go func() {
		for {
			ms.subChan <- ms.subClient.Receive()
		}
	}()

	go func() {
		for {
			time.Sleep(time.Duration(ms.TickInMilliseconds) * time.Millisecond)
			ms.timeout <- true
		}
	}()

	go func() {
		var sr *pubsub.SubResp
		for {
			select {
			case sr = <-ms.subChan:
				if db1 {
					fmt.Fprintf(os.Stderr, "%s**** Got a message, sr=%+v, is --->>>%s<<<---, AT:%s%s\n", MiscLib.ColorGreen, godebug.SVar(sr), sr.Message, godebug.LF(), MiscLib.ColorReset)
				}

				var mm MsMessageToSend
				arb := make(map[string]string)
				err := json.Unmarshal([]byte(sr.Message), &arb)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%sError: %s --->>>%s<<<--- AT: %s%s\n", MiscLib.ColorRed, err, sr.Message, godebug.LF(), MiscLib.ColorReset)
				} else {

					errFound := false
					{
						key := arb["Key"]

						conn := ms.GetRedis()
						defer ms.PutRedis(conn)

						v, err := conn.Cmd("GET", key).Str()
						if err != nil {
							if db4 {
								fmt.Fprintf(os.Stderr, "%sError on redis - did not find reply key - get(%s): %s, %s%s\n", MiscLib.ColorRed, key, err, godebug.LF(), MiscLib.ColorReset)
							}
							errFound = true
						}

						err = json.Unmarshal([]byte(v), &mm)
						if err != nil {
							fmt.Fprintf(os.Stderr, "%sError parsing reply message, Err=%s, Msg=%s %s\n", MiscLib.ColorRed, err, v, godebug.LF(), MiscLib.ColorReset)
							errFound = true
						}
						fmt.Fprintf(os.Stderr, "!!! Reply Message Parsed --->>%s<<---\n", lib.SVar(mm))
					}

					// xyzzy - check if this is a dead id (timedout)
					//hasTimedOut, ok :=  ms.timeOutId[id]		// xyzzy - lock/unlock
					//if hasTimedOut && ok {
					//} else {
					//	// xyzzy - call the doReply function with this
					//}

					if !errFound {
						if rx, ok := ms.ReplyFx[mm.CallId]; ok {
							rx.Fx(&mm, rx, false)
							delete(ms.ReplyFx, mm.CallId)
						}
					}

				}

			case <-ms.timeout: // the read from ms.subChan has timed out

				if ms.DebugTimeoutMessage {
					fmt.Fprintf(os.Stderr, "%s**** Got a timeout, AT:%s%s\n", MiscLib.ColorGreen, godebug.LF(), MiscLib.ColorReset)
				}

				// need to count "ticks" and if exceeds this number of "ticks" then it is timed out.
				// iterate over the ms.ReplyFx and count up

				for key, vv := range ms.ReplyFx {
					if vv.TickCount >= vv.TickMax {
						vv.Fx(nil, vv, true)
						delete(ms.ReplyFx, key)
					} else {
						vv.TickCount++
						ms.ReplyFx[key] = vv
					}
				}

				//for key, vv := range ms.ReplyFx {
				//	if vv.TickCount > (100*vv.TickMax) || vv.IsDead {
				//	}
				//}
			}
		}
	}()

}

func (ms *MsCfgType) ListenForServer(doWork WorkFuncType, wg *sync.WaitGroup) { // server *socketio.Server) {

	arb := make(map[string]interface{})
	arb["cmd"] = "at-top"
	doWork(arb)

	go func() {
		for {
			ms.subChan <- ms.subClient.Receive()
		}
	}()

	go func() {
		for {
			time.Sleep(time.Duration(ms.TickInMilliseconds) * time.Millisecond)
			ms.timeout <- true
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		var sr *pubsub.SubResp
		counter := 0
		threshold := 100 // xyzzy - from config!!!
		for {
			select {
			case sr = <-ms.subChan:
				if db1 {
					fmt.Fprintf(os.Stderr, "%s**** Got a message, sr=%+v, is --->>>%s<<<---, AT:%s%s\n", MiscLib.ColorGreen, godebug.SVar(sr), sr.Message, godebug.LF(), MiscLib.ColorReset)
				}

				arb := make(map[string]interface{})
				err := json.Unmarshal([]byte(sr.Message), &arb)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%sError: %s --->>>%s<<<--- AT: %s%s\n", MiscLib.ColorRed, err, sr.Message, godebug.LF(), MiscLib.ColorReset)
				} else {

					cmd := ""
					cmd_x, ok := arb["cmd"]
					if ok {
						cmd_s, ok := cmd_x.(string)
						if ok {
							cmd = cmd_s
						}
					}
					if cmd == "exit-now" {
						break
					}

					doWork(arb)

				}

			case <-ms.timeout: // the read from ms.subChan has timed out

				if ms.DebugTimeoutMessage {
					fmt.Fprintf(os.Stderr, "%s**** Got a timeout, AT:%s%s\n", MiscLib.ColorGreen, godebug.LF(), MiscLib.ColorReset)
				}

				// If stuck doing work - may need to kill/restart - server side timeout.
				counter++
				if counter > threshold {
					if repoll_db {
						fmt.Fprintf(os.Stderr, "%s**** timeout - results in a call to doWork(), AT:%s%s\n", MiscLib.ColorGreen, godebug.LF(), MiscLib.ColorReset)
					}
					arb := make(map[string]interface{})
					arb["cmd"] = "timeout-call"
					doWork(arb)
					counter = 0
				}

			}
		}
	}()

}

// SetReplyFunc needs to be called before sending a message if you want a call/responce type operation.
// If you want message send and forget then do not call this with a 'fx', leave ms.ReplyFunc nil.
func (ms *MsCfgType) SetReplyFunc(fx func(replyMessage *MsMessageToSend)) {
	ms.ReplyFunc = fx
}

// ReceiveGroupReply() is the way to receive a reply to a group or timeout reply to a group of message.  It will call it's worker function as
// soon as all of the group have had a reply or when the timeout specified occures.
//
// The replay pub/sub is w-r:Name
// The replay data is w-d:Id - this is a set/list of data that are replies -- Reply Template
func (ms *MsCfgType) ReceiveGroupReply() {
	// xyzzy -- suppose send sends a set of messages and then this thigie creates a set of go-routines that wait for individual call backs.
	// each of these is waited for as a go-routine.  And the results are collected.  Any of them that time out do so with no results.
}

// SvrListenForMessage() is the serve function that performs
// 1.  Listen on the Redis pub/sub for wakie-wakie
// 2.  Periodic self-ping to wake up and check Q (1ce every 10 sec - configurable)
func (ms *MsCfgType) SvrListenForMessage() {
}

func (ms *MsCfgType) Dump() {
	fmt.Printf("Dump out internal information on ms\n")
	fmt.Printf("ReplyFx = %s\n\n", godebug.SVarI(ms.ReplyFx))
}

func (hdlr *MsCfgType) ConnectToRedis() bool {
	// Note: best test for this is in the TabServer2 - test 0001 - checks that this works.
	var err error

	dflt := func(a string, d string) (rv string) {
		rv = a
		if rv == "" {
			rv = d
		}
		return
	}

	redis_host := dflt(hdlr.RedisConnectHost, "127.0.0.1")
	redis_port := dflt(hdlr.RedisConnectPort, "6379")
	redis_auth := hdlr.RedisConnectAuth

	if redis_auth == "" { // If Redis AUTH section
		hdlr.RedisPool, err = pool.New("tcp", redis_host+":"+redis_port, 20)
	} else {
		hdlr.RedisPool, err = pool.NewAuth("tcp", redis_host+":"+redis_port, 20, redis_auth)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: Failed to connect to redis-server.%s\n", MiscLib.ColorRed, MiscLib.ColorReset)
		fmt.Printf("Error: Failed to connect to redis-server.\n")
		// goftlmux.G_Log.Info("Error: Failed to connect to redis-server.\n")
		// logrus.Fatalf("Error: Failed to connect to redis-server.\n")
		return false
	} else {
		if db11 {
			fmt.Fprintf(os.Stderr, "%sSuccess: Connected to redis-server.%s\n", MiscLib.ColorGreen, MiscLib.ColorReset)
		}
	}

	return true
}

/*
-------------------------------------------------------
UUID Notes
-------------------------------------------------------

import (
	newUuid "github.com/pborman/uuid"
	"github.com/pschlump/Go-FTL/server/lib"
	"github.com/taskcluster/slugid-go/slugid"
)

	id0 := lib.GetUUIDAsString()
	id0_slug := UUIDToSlug(id0)
*/

func UUIDToSlug(uuid string) (slug string) {
	// slug = id
	uuidType := newUuid.Parse(uuid)
	if uuidType != nil {
		slug = slugid.Encode(uuidType)
		return
	}
	fmt.Fprintf(os.Stderr, "slug: ERROR: Cannot encode invalid uuid '%v' into a slug\n", uuid) // Xyzzy - logrus
	return
}

func SlugToUUID(slug string) (uuid string) {
	// uuid = slug
	uuidType := slugid.Decode(slug)
	if uuidType != nil {
		uuid = uuidType.String()
		return
	}
	fmt.Fprintf(os.Stderr, "slug: ERROR: Cannot decode invalid slug '%v' into a UUID\n", slug) // Xyzzy - logrus
	return
}

func StructToJson(mm interface{}) (rv string) {
	trv, err := json.Marshal(mm)
	if err != nil {
		rv = "{}"
		return
	}
	rv = string(trv)
	return
}

// --------------------------------------------------------------------------------------------------------------------------------

// ms.AddToFnMap(ms.GenCacheFn(id), url)
func (hdlr *MsCfgType) AddToFnMap(replace, fn, origFn, mt string) {

	fmt.Printf("AddToFnMap: replace=%s, fn=%s, %s\n", replace, fn, godebug.LF())

	conn := hdlr.GetRedis()
	defer hdlr.PutRedis(conn)

	// Replace			fn
	// From /ipfs/ID -> /t1/css/t1.css
	// From /ipfs/ID -> http://www.example.com/t1/css/t1.css
	type FCacheType struct {
		FileName       string `json:"FileName"`      // should change json to just "fn"
		State          string `json:"St,omitempty"`  //
		OrigFileName   string `json:"ofn,omitempty"` // if this has not been fetched yet, then 307 to this, Orig starts with http[s]?//...
		RemoteFileName string `json:"rfn,omitempty"` // if this has not been fetched yet, then 307 to this, Orig starts with http[s]?//...
		Replace        string `json:"rp,omitempty"`  //
		MimeType       string `json:"mt,omitempty"`  // The mime type of the file
	}

	key := "h2p3:" + replace
	// FCacheData := H2pppCommon.FCacheType{FileName: fn, Replace: replace, State: "200", OrigFileName: origFn, RemoteFileName: fn, MimeType: mt}
	FCacheData := FCacheType{FileName: fn, Replace: replace, State: "200", OrigFileName: origFn, RemoteFileName: fn, MimeType: mt}

	value := ""
	v, err := json.Marshal(FCacheData)
	if err != nil {
		value = "{}"
	} else {
		value = string(v)
	}

	err = conn.Cmd("SET", key, value).Err
	if err != nil {
		if db4 {
			fmt.Printf("Error on redis - file data not saved - get(%s): %s\n", key, err)
		}
		return
	}

	//	// fn						replace
	//	// From /t1/css/t1.css   -> /ipfs/ID
	//	// From http://www.example.com/t1/css/t1.css -> /ipfs/ID
	//	key1 := "h2p3:" + fn
	//	FCacheData := H2pppCommon.FCacheType{FileName: replace, State: "200", OrigFileName: fn, RemoteFileName: replace}
	//
	//	value1 := ""
	//	v, err := json.Marshal(FCacheData)
	//	if err != nil {
	//		value1 = "{}"
	//	} else {
	//		value1 = string(v)
	//	}
	//
	//	err = conn.Cmd("SET", key1, value1).Err
	//	if err != nil {
	//		if db4 {
	//			fmt.Printf("Error on redis - file data not saved - get(%s): %s\n", key1, err)
	//		}
	//		return
	//	}
	//
	//	fmt.Printf("Redis: SET %s %s, and... reverse %s %s\n", key, value, key1, value1)

	fmt.Printf("Redis: SET %s %s\n", key, value)
}

// ms.AddToFnMap(ms.GenCacheFn(id, path), url)
func (hdlr *MsCfgType) GenCacheFn(id, pth, urlPath string) string {
	return urlPath + "/" + id
}

func GetParam(Params []MsMessageParams, name string) string {
	for _, vv := range Params {
		if vv.Name == name {
			return vv.Value
		}
	}
	return ""
}

func GetParamIndex(Params []MsMessageParams, name string) int {
	for ii, vv := range Params {
		if vv.Name == name {
			return ii
		}
	}
	return -1
}

const ReturnPacked = true

func UUIDAsStr() (s_id string) {
	id, _ := uuid.NewV4()
	s_id = id.String()
	return
}

func UUIDAsStrPacked() (s_id string) {
	if ReturnPacked {
		s := UUIDAsStr()
		return UUIDToSlug(s)
	} else {
		return UUIDAsStr()
	}
}

const db1 = false
const db4 = false
const db11 = false
const db12 = true
const repoll_db = false

/* vim: set noai ts=4 sw=4: */
