package logkeeper

import (
	"context"
	"testing"
	"time"

	"github.com/evergreen-ci/logkeeper/db"
	"github.com/evergreen-ci/logkeeper/env"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func initTestDB(ctx context.Context, t *testing.T) {
	client, err := mongo.NewClient(options.Client().ApplyURI("mongodb://localhost:27017"))
	require.NoError(t, err)
	require.NoError(t, client.Connect(ctx))

	env.SetClient(client)
	env.SetDBName("logkeeper_test")
	env.SetContext(ctx)
}

func clearCollections(t *testing.T, collections ...string) {
	for _, col := range collections {
		_, err := db.C(col).DeleteMany(env.Context(), bson.M{})
		require.NoError(t, err)
	}
}

func insertBuilds(t *testing.T) []string {
	assert := assert.New(t)

	info := make(map[string]interface{})
	info["task_id"] = primitive.NewObjectID().Hex()
	now := time.Now()
	oldBuild1 := LogKeeperBuild{
		Id:      "one",
		Started: time.Date(2016, time.January, 15, 0, 0, 0, 0, time.Local),
		Info:    info,
	}
	oldBuild2 := LogKeeperBuild{
		Id:      "two",
		Started: time.Date(2016, time.February, 15, 0, 0, 0, 0, time.Local),
		Info:    info,
	}
	edgeBuild := LogKeeperBuild{
		Id:      "three",
		Started: now.Add(-deletePassedTestCutoff + time.Minute),
		Failed:  false,
		Info:    info,
	}
	newBuild := LogKeeperBuild{
		Id:      "four",
		Started: now,
		Info:    info,
	}
	_, err := db.C(buildsCollection).InsertMany(env.Context(), []interface{}{oldBuild1, oldBuild2, edgeBuild, newBuild})
	assert.NoError(err)
	return []string{oldBuild1.Id, oldBuild2.Id, edgeBuild.Id, newBuild.Id}
}

func insertTests(t *testing.T, ids []string) {
	assert := assert.New(t)

	test1 := Test{
		Id:      primitive.NewObjectID(),
		BuildId: ids[0],
	}
	test2 := Test{
		Id:      primitive.NewObjectID(),
		BuildId: ids[1],
	}
	test3 := Test{
		Id:      primitive.NewObjectID(),
		BuildId: ids[2],
	}
	test4 := Test{
		Id:      primitive.NewObjectID(),
		BuildId: ids[3],
	}
	_, err := db.C(testsCollection).InsertMany(env.Context(), []interface{}{test1, test2, test3, test4})
	assert.NoError(err)
}

func insertLogs(t *testing.T, ids []string) {
	assert := assert.New(t)

	log1 := Log{BuildId: ids[0]}
	log2 := Log{BuildId: ids[0]}
	log3 := Log{BuildId: ids[1]}
	newId := primitive.NewObjectID().Hex()
	log4 := Log{BuildId: newId}
	_, err := db.C(logsCollection).InsertMany(env.Context(), []interface{}{log1, log2, log3, log4})
	assert.NoError(err)
}

func TestGetOldTests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initTestDB(ctx, t)
	clearCollections(t, buildsCollection)

	assert := assert.New(t)
	ids := insertBuilds(t)
	insertTests(t, ids)
	insertLogs(t, ids)

	builds, err := GetOldBuilds(CleanupBatchSize)
	assert.NoError(err)
	assert.Len(builds, 2)
}

func TestCleanupOldLogsAndTestsByBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initTestDB(ctx, t)
	clearCollections(t, buildsCollection, testsCollection, logsCollection)

	assert := assert.New(t)

	ids := insertBuilds(t)
	insertTests(t, ids)
	insertLogs(t, ids)

	count, _ := db.C(testsCollection).CountDocuments(env.Context(), bson.M{})
	assert.EqualValues(4, count)

	count, _ = db.C(logsCollection).CountDocuments(env.Context(), bson.M{})
	assert.EqualValues(4, count)

	deletedStats, err := CleanupOldLogsAndTestsByBuild(ids[0])
	assert.NoError(err)
	assert.Equal(1, deletedStats.NumBuilds)
	assert.Equal(1, deletedStats.NumTests)
	assert.Equal(2, deletedStats.NumLogs)

	count, _ = db.C(testsCollection).CountDocuments(env.Context(), bson.M{})
	assert.EqualValues(3, count)

	count, _ = db.C(logsCollection).CountDocuments(env.Context(), bson.M{})
	assert.EqualValues(2, count)
}

func TestNoErrorWithNoLogsOrTests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initTestDB(ctx, t)
	clearCollections(t, testsCollection)

	assert := assert.New(t)

	test := Test{
		Id:      primitive.NewObjectID(),
		BuildId: "incompletebuild",
		Started: time.Now(),
	}
	build := LogKeeperBuild{Id: "incompletebuild"}
	_, err := db.C(buildsCollection).InsertOne(env.Context(), build)
	assert.NoError(err)
	_, err = db.C(testsCollection).InsertOne(env.Context(), test)
	assert.NoError(err)
	deletedStats, err := CleanupOldLogsAndTestsByBuild(test.BuildId)
	assert.NoError(err)
	assert.Equal(1, deletedStats.NumBuilds)
	assert.Equal(1, deletedStats.NumTests)
	assert.Equal(0, deletedStats.NumLogs)

	log := Log{BuildId: "incompletebuild"}
	_, err = db.C(buildsCollection).InsertOne(env.Context(), build)
	assert.NoError(err)
	_, err = db.C(logsCollection).InsertOne(env.Context(), log)
	assert.NoError(err)
	deletedStats, err = CleanupOldLogsAndTestsByBuild(log.BuildId)
	assert.NoError(err)
	assert.Equal(1, deletedStats.NumBuilds)
	assert.Equal(0, deletedStats.NumTests)
	assert.Equal(1, deletedStats.NumLogs)
}

func TestUpdateFailedTest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initTestDB(ctx, t)
	clearCollections(t, buildsCollection, testsCollection, logsCollection)

	assert := assert.New(t)

	ids := insertBuilds(t)
	insertTests(t, ids)
	insertLogs(t, ids)

	builds, err := GetOldBuilds(CleanupBatchSize)
	assert.NoError(err)
	assert.Len(builds, 2)

	err = UpdateFailedBuild(ids[1])
	assert.NoError(err)
	builds, err = GetOldBuilds(CleanupBatchSize)
	assert.NoError(err)
	assert.Len(builds, 1)
}
