package database

import (
	"bytes"
	"encoding/gob"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/tmaiaroto/discfg/config"
	// "log"
	"os"
	"strconv"
	"time"
)

// DynamoDB implements the Shipper interface.
type DynamoDB struct {
}

// Svc configures the DynamoDB service to use
func Svc(opts config.Options) *dynamodb.DynamoDB {
	awsConfig := &aws.Config{Region: aws.String(opts.Storage.AWS.Region)}

	// If a session was passed... (AWS Lambda does this)
	if opts.Storage.AWS.SessionToken != "" {
		os.Setenv("AWS_SESSION_TOKEN", opts.Storage.AWS.SessionToken)
	}

	// Look in a variety of places for AWS credentials. First, try the credentials file set by AWS CLI tool.
	// Note the empty string instructs to look under default file path (different based on OS).
	// This file can have multiple profiles and a default profile will be used unless otherwise configured.
	// See: https://godoc.org/github.com/aws/aws-sdk-go/aws/credentials#SharedCredentialsProvider
	creds := credentials.NewSharedCredentials("", opts.Storage.AWS.CredProfile)
	_, err := creds.Get()
	// If that failed, try environment variables.
	if err != nil {
		// The following are checked:
		// Access Key ID: AWS_ACCESS_KEY_ID or AWS_ACCESS_KEY
		// Secret Access Key: AWS_SECRET_ACCESS_KEY or AWS_SECRET_KEY
		creds = credentials.NewEnvCredentials()
	}

	// If credentials were passed via config, then use those. They will take priority over other methods.
	if opts.Storage.AWS.AccessKeyID != "" && opts.Storage.AWS.SecretAccessKey != "" {
		creds = credentials.NewStaticCredentials(opts.Storage.AWS.AccessKeyID, opts.Storage.AWS.SecretAccessKey, "")
	}
	awsConfig.Credentials = creds

	return dynamodb.New(session.New(awsConfig))
}

// CreateConfig creates a new table for a configuration
func (db DynamoDB) CreateConfig(opts config.Options, settings map[string]interface{}) (interface{}, error) {
	svc := Svc(opts)
	wu := int64(1)
	ru := int64(2)
	if val, ok := settings["WriteCapacityUnits"]; ok {
		wu = int64(val.(float64))
	}
	if val, ok := settings["ReadCapacityUnits"]; ok {
		ru = int64(val.(float64))
	}

	// Must be at least 1
	if wu < 1 {
		wu = int64(1)
	}
	// Also must be at least 1, default to 2
	if ru < 1 {
		ru = int64(2)
	}

	params := &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("key"),
				AttributeType: aws.String("S"),
			},
			// {
			// 	AttributeName: aws.String("id"),
			// 	AttributeType: aws.String("N"),
			// },
			// // More values...
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			// One is required, but we use both a HASH (key name) and a RANGE (Snowflake).
			{
				AttributeName: aws.String("key"),
				KeyType:       aws.String("HASH"),
			},
			// {
			// 	AttributeName: aws.String("id"),
			// 	KeyType:       aws.String("RANGE"),
			// },
		},
		// Hard to estimate really. Should be passed along via command line when creating a new config.
		// Along with the table name. This will let people choose. Though it's kinda annoying someone must
		// think about this...
		// http://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ProvisionedThroughputIntro.html
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{ // Required
			ReadCapacityUnits:  aws.Int64(ru), // Required
			WriteCapacityUnits: aws.Int64(wu), // Required
		},
		TableName: aws.String(opts.CfgName), // Required

	}
	response, err := svc.CreateTable(params)
	// TODO: Convey this somehow?
	// if err == nil {
	// tableStatus := *response.TableDescription.TableStatus
	// if tableStatus != "CREATING" && tableStatus != "ACTIVE" {
	// 	err = errors.New("Something went wrong creating tables")
	// }
	// }
	return response, err
}

// DeleteConfig deletes a configuration (removing the DynamoDB table and all data within it)
func (db DynamoDB) DeleteConfig(opts config.Options) (interface{}, error) {
	svc := Svc(opts)
	params := &dynamodb.DeleteTableInput{
		TableName: aws.String(opts.CfgName), // Required
	}
	return svc.DeleteTable(params)
}

// UpdateConfig updates a configuration (DyanmoDB can have its read and write capacity units adjusted as needed)
// Note: Adjusting the read capacity is fast, adjusting write capacity takes longer.
func (db DynamoDB) UpdateConfig(opts config.Options, settings map[string]interface{}) (interface{}, error) {
	svc := Svc(opts)
	wu := int64(1)
	ru := int64(2)
	if val, ok := settings["WriteCapacityUnits"]; ok {
		wu = int64(val.(float64))
	}
	if val, ok := settings["ReadCapacityUnits"]; ok {
		ru = int64(val.(float64))
	}
	// Must be at least 1
	if wu < 1 {
		wu = int64(1)
	}
	// Also must be at least 1, default to 2
	if ru < 1 {
		ru = int64(2)
	}

	params := &dynamodb.UpdateTableInput{
		TableName: aws.String(opts.CfgName), // Required
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(ru), // Required
			WriteCapacityUnits: aws.Int64(wu), // Required
		},
		// Not for now. Mainly because only one operation per UpdateTable() call. Makes it annoying.
		// StreamSpecification: &dynamodb.StreamSpecification{
		// 	StreamEnabled:  aws.Bool(true),
		// 	StreamViewType: aws.String("StreamViewType"),
		// },
	}
	return svc.UpdateTable(params)
}

// ConfigState returns the DynamoDB table state
func (db DynamoDB) ConfigState(opts config.Options) (string, error) {
	svc := Svc(opts)
	status := ""

	params := &dynamodb.DescribeTableInput{
		TableName: aws.String(opts.CfgName), // Required
	}
	resp, err := svc.DescribeTable(params)
	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		//fmt.Println(err.Error())
		status = *resp.Table.TableStatus
	}
	return status, err
}

// Update a key in DynamoDB
func (db DynamoDB) Update(opts config.Options) (config.Node, error) {
	var err error
	svc := Svc(opts)
	node := config.Node{Key: opts.Key}

	// log.Println("Setting on table name: " + name)
	// log.Println(opts.Key)
	// log.Println(opts.Value)

	ttlString := strconv.FormatInt(opts.TTL, 10)
	expires := time.Now().Add(time.Duration(opts.TTL) * time.Second)
	expiresInt := expires.UnixNano()
	expiresString := strconv.FormatInt(expiresInt, 10)
	// If no TTL was passed in the options, set 0. Anything 0 is indefinite in these cases.
	if opts.TTL == 0 {
		expiresString = "0"
	}

	//log.Println(value)
	// DynamoDB type cheat sheet:
	// B: []byte("some bytes")
	// BOOL: aws.Bool(true)
	// BS: [][]byte{[]byte("bytes and bytes")}
	// L: []*dynamodb.AttributeValue{{...recursive values...}}
	// M: map[string]*dynamodb.AttributeValue{"key": {...recursive...} }
	// N: aws.String("number")
	// NS: []*String{aws.String("number"), aws.String("number")}
	// NULL: aws.Bool(true)
	// S: aws.String("string")
	// SS: []*string{aws.String("string"), aws.String("string")}

	// If always putting new items, there's no conditional update.
	// But the only way to update is to make the items have a HASH only index instead of HASH + RANGE.

	params := &dynamodb.UpdateItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"key": {
				S: aws.String(opts.Key),
			},
		},
		TableName: aws.String(opts.CfgName),
		// KEY and VALUE are reserved words so the query needs to dereference them
		ExpressionAttributeNames: map[string]*string{
			//"#k": aws.String("key"),
			"#v": aws.String("value"),
			// If TTL is a reserved word in DynamoDB...Then why doesn't it seem to have a TTL feature??
			"#t": aws.String("ttl"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			// value
			":value": {
				//B: []byte(opts.Value), // <-- sure, if all we ever stored as strings.
				B: opts.Value,
			},
			// TTL
			":ttl": {
				N: aws.String(ttlString),
			},
			// Expiration timestamp
			":expires": {
				N: aws.String(expiresString),
			},
			// version increment
			":i": {
				N: aws.String("1"),
			},
		},
		//ReturnConsumedCapacity:      aws.String("TOTAL"),
		//ReturnItemCollectionMetrics: aws.String("ReturnItemCollectionMetrics"),
		ReturnValues:     aws.String("ALL_OLD"),
		UpdateExpression: aws.String("SET #v = :value, #t = :ttl, expires = :expires ADD version :i"),
	}

	// Conditional write operation (CAS)
	if opts.ConditionalValue != "" {
		params.ExpressionAttributeValues[":condition"] = &dynamodb.AttributeValue{B: []byte(opts.ConditionalValue)}
		params.ConditionExpression = aws.String("#v = :condition")
	}

	response, err := svc.UpdateItem(params)
	if err == nil {
		// The old values
		if val, ok := response.Attributes["value"]; ok {
			node.Value = val.B
			node.Version, _ = strconv.ParseInt(*response.Attributes["version"].N, 10, 64)
		}
	}

	return node, err
}

// Get a key in DynamoDB
func (db DynamoDB) Get(opts config.Options) (config.Node, error) {
	var err error
	svc := Svc(opts)
	node := config.Node{Key: opts.Key}

	params := &dynamodb.QueryInput{
		TableName: aws.String(opts.CfgName),

		// KEY and VALUE are reserved words so the query needs to dereference them
		ExpressionAttributeNames: map[string]*string{
			"#k": aws.String("key"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":key": {
				S: aws.String(opts.Key),
			},
		},
		KeyConditionExpression: aws.String("#k = :key"),
		// TODO: Return more? It's nice to have a history now whereas previously I thought I might now have one...But what's the use?
		Limit: aws.Int64(1),

		// INDEXES | TOTAL | NONE (not required - not even sure if I need to worry about it)
		ReturnConsumedCapacity: aws.String("TOTAL"),
		// Important: This needs to be false so it returns results in descending order. If it's true (the default), it's sorted in the
		// order values were stored. So the first item stored for the key ever would be returned...But the latest item is needed.
		ScanIndexForward: aws.Bool(false),
		// http://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html#DDB-Query-request-Select
		Select: aws.String("ALL_ATTRIBUTES"),
	}
	response, err := svc.Query(params)

	if err != nil {
		// Print the error, cast err to awserr.Error to get the Code and
		// Message from an error.
		//fmt.Println(err.Error())

		if len(response.Items) > 0 {
			// Every field should now be checked because it's possible to have a response without a value or version.
			// For example, the root key "/" may only hold information about the config version and modified time.
			// It may not have a set value and therefore it also won't have a relative version either.
			// TODO: Maybe it should? We can always version it as 1 even if empty value. Perhaps also an empty string value...
			// But the update config version would need to have a compare for an empty value. See if DynamoDB can do that.
			// For now, just check the existence of keys in the map.
			if val, ok := response.Items[0]["value"]; ok {
				node.Value = val.B
			}
			if val, ok := response.Items[0]["version"]; ok {
				node.Version, _ = strconv.ParseInt(*val.N, 10, 64)
			}

			// Expiration/TTL (only set if > 0)
			if val, ok := response.Items[0]["ttl"]; ok {
				ttl, _ := strconv.ParseInt(*val.N, 10, 64)
				if ttl > 0 {
					node.TTL = ttl
				}
			}
			if val, ok := response.Items[0]["expires"]; ok {
				expiresNano, _ := strconv.ParseInt(*val.N, 10, 64)
				if expiresNano > 0 {
					node.Expiration = time.Unix(0, expiresNano)
				}
			}

			// If cfgVersion and cfgModified are set because it's the root key "/" then set those too.
			// This is only returned for the root key. no sense in making a separate get function because operations like
			// exporting would then require more queries than necessary. However, it won't be displayed in the node's JSON output.
			if val, ok := response.Items[0]["cfgVersion"]; ok {
				node.CfgVersion, _ = strconv.ParseInt(*val.N, 10, 64)
			}
			if val, ok := response.Items[0]["cfgModified"]; ok {
				node.CfgModifiedNanoseconds, _ = strconv.ParseInt(*val.N, 10, 64)
			}
		}

		// Check the TTL
		if node.TTL > 0 {
			// If expired, return an empty node
			if node.Expiration.UnixNano() < time.Now().UnixNano() {
				node = config.Node{Key: opts.Key}
				// Delete the now expired item
				// NOTE: This does mean waiting on another DynamoDB request and that technically means slower performance in these situations, but is it a conern?
				// A goroutine doesn't help because there's not guarantee there's time for it to complete.
				db.Delete(opts)
			}
		}
	}

	return node, err
}

func getChildren(svc *dynamodb.DynamoDB, opts config.Options) ([]config.Node, error) {
	var err error
	nodes := []config.Node{}

	// TODO
	// params := &dynamodb.QueryInput{
	// 	TableName: aws.String(opts.CfgName),

	// 	// KEY and VALUE are reserved words so the query needs to dereference them
	// 	ExpressionAttributeNames: map[string]*string{
	// 		"#k": aws.String("key"),
	// 	},
	// 	ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
	// 		":key": {
	// 			S: aws.String(opts.Key),
	// 		},
	// 	},
	// 	KeyConditionExpression: aws.String("#k = :key"),
	// 	// TODO: Return more? It's nice to have a history now whereas previously I thought I might now have one...But what's the use?
	// 	Limit: aws.Int64(1),

	// 	// INDEXES | TOTAL | NONE (not required - not even sure if I need to worry about it)
	// 	ReturnConsumedCapacity: aws.String("TOTAL"),
	// 	// Important: This needs to be false so it returns results in descending order. If it's true (the default), it's sorted in the
	// 	// order values were stored. So the first item stored for the key ever would be returned...But the latest item is needed.
	// 	ScanIndexForward: aws.Bool(false),
	// 	// http://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html#DDB-Query-request-Select
	// 	Select: aws.String("ALL_ATTRIBUTES"),
	// }
	// response, err := svc.Query(params)

	return nodes, err
}

// Delete a key in DynamoDB
func (db DynamoDB) Delete(opts config.Options) (config.Node, error) {
	var err error
	svc := Svc(opts)
	node := config.Node{Key: opts.Key}

	params := &dynamodb.DeleteItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"key": {
				S: aws.String(opts.Key),
			},
		},
		TableName:    aws.String(opts.CfgName),
		ReturnValues: aws.String("ALL_OLD"),
		// TODO: think about this for statistics
		// INDEXES | TOTAL | NONE
		//ReturnConsumedCapacity: aws.String("ReturnConsumedCapacity"),
	}

	// Conditional delete operation
	if opts.ConditionalValue != "" {
		// Alias value since it's a reserved word
		params.ExpressionAttributeNames = make(map[string]*string)
		params.ExpressionAttributeNames["#v"] = aws.String("value")
		// Set the condition expression value and compare
		params.ExpressionAttributeValues = make(map[string]*dynamodb.AttributeValue)
		params.ExpressionAttributeValues[":condition"] = &dynamodb.AttributeValue{B: []byte(opts.ConditionalValue)}
		params.ConditionExpression = aws.String("#v = :condition")
	}

	response, err := svc.DeleteItem(params)
	if err == nil {
		if len(response.Attributes) > 0 {
			node.Value = response.Attributes["value"].B
			node.Version, _ = strconv.ParseInt(*response.Attributes["version"].N, 10, 64)
		}
	}

	return node, err
}

// UpdateConfigVersion updates the configuration's global version and modified timestamp (fields unique to the root key "/")
func (db DynamoDB) UpdateConfigVersion(opts config.Options) error {
	svc := Svc(opts)
	now := time.Now()
	params := &dynamodb.UpdateItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"key": {
				S: aws.String("/"),
			},
		},
		TableName: aws.String(opts.CfgName),
		ExpressionAttributeNames: map[string]*string{
			"#m": aws.String("cfgModified"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			// modified timestamp (DynamoDB has no date type)
			":modified": {
				N: aws.String(strconv.FormatInt(now.UnixNano(), 10)),
			},
			// version increment
			":i": {
				N: aws.String("1"),
			},
		},
		ReturnValues:     aws.String("NONE"),
		UpdateExpression: aws.String("SET #m = :modified ADD cfgVersion :i"),
	}
	_, err := svc.UpdateItem(params)
	return err
}

// Prepares data to be stored in DynamoDb as byte array. interface{} -> []byte
// DEPRECATED
func getBytes(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
