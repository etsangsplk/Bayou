package bayou

import (
    "fmt"
    "net/rpc"
    "os"
    "path/filepath"
    "sync"
    "testing"
)

/*************************
 *    HELPER METHODS     *
 *************************/

/* Fails provided test if condition is not true */
func assert(t *testing.T, cond bool, message string) {
    if !cond {
        t.Fatal(message)
    }
}

/* Fails provided test if a and b are not equal */
func assertEqual(t *testing.T, a interface{}, b interface{}, message string) {
    assert(t, a == b, message)
}

/* Fails provided test if err is not nil */
func ensureNoError(t *testing.T, err error, prefix string) {
    if err != nil {
        t.Fatal(prefix + err.Error())
    }
}

/*************************
 *    DATABASE TESTS     *
 *************************/

/* Returns the Bayou database with the provided filename *
 * Clears the database before returning if reset is true *
 * All test databases are stored in the "db" directory   */
func getDB(filename string, reset bool) *BayouDB {
    dirname := "db"
    os.MkdirAll(dirname, os.ModePerm)
    dbFilepath := filepath.Join(dirname, filename)
    if reset {
        os.RemoveAll(dbFilepath)
    }
    return InitDB(dbFilepath)
}

/* Fails provided test if rooms are not equal */
func assertRoomsEqual(t *testing.T, room Room, exp Room) {
    failMsg := "Expected Room: " + exp.String() +
            "\tReceived: " + room.String()
    if (room.Name != exp.Name) ||
       !timesEqual(room.StartTime, exp.StartTime) ||
       !timesEqual(room.EndTime, exp.EndTime) {
        t.Fatal(failMsg)
    }
}

/* Tests basic database functionality */
func TestUnitDBBasic(t *testing.T) {
    // Open the Database
    const dbpath = "dbbasic.db"
    db := getDB(dbpath, true)
    defer db.Close()

    name := "Fine"
    startDate := createDate(0, 0)
    endDate := createDate(1, 5)
    startTxt := startDate.Format("2006-01-02 15:04")
    endTxt   := endDate.Format("2006-01-02 15:04")

    // Execute insertion query
    query := fmt.Sprintf(`
    INSERT OR REPLACE INTO rooms(
        Name,
        StartTime,
        EndTime
    ) values("%s", dateTime("%s"), dateTime("%s"))
    `, name, startTxt, endTxt)
    db.Execute(query)

    // Execute read query
    readQuery := `
        SELECT Name, StartTime, EndTime
        FROM rooms
    `
    result := db.Read(readQuery)

    // Ensure results are as expected
    rooms := deserializeRooms(result)
    assertEqual(t, len(rooms), 1, "Read query returned wrong number of rooms.")
    assertRoomsEqual(t, rooms[0], Room{name, startDate, endDate})

    // Execute a dependency check query
    check := fmt.Sprintf(`
    SELECT CASE WHEN EXISTS (
            SELECT *
            FROM rooms
            WHERE StartTime BETWEEN dateTime("%s") AND dateTime("%s")
    )
    THEN CAST(1 AS BIT)
    ELSE CAST(0 AS BIT) END
    `, startTxt, endTxt);
    assert(t, db.Check(check), "Dependency check failed.")

    // Execute a merge check query
    merge := `
    SELECT 0
    `
    assert(t, !db.Check(merge), "Merge check failed.")
}

/*****************************
 *    VECTOR CLOCK TESTS     *
 *****************************/

/* Fails provided test if VCs are not equal */
func assertVCsEqual(t *testing.T, vc VectorClock, exp VectorClock) {
    failMsg := "Expected VC: " + exp.String() + "\tReceived: " + vc.String()
    if len(vc) != len(exp) {
        t.Fatal(failMsg)
    }
    for idx, _ := range vc {
        if vc[idx] != exp[idx] {
            t.Fatal(failMsg)
        }
    }
}

/* Unit tests vector clock */
func TestUnitVectorClock(t *testing.T) {
    vc := NewVectorClock(4)
    assertVCsEqual(t, vc, VectorClock{0, 0, 0, 0})

    // Ensure Inc works as expected
    vc.Inc(1)
    vc.Inc(3)
    vc.Inc(3)
    assertVCsEqual(t, vc, VectorClock{0, 1, 0, 2})

    // Ensure Set works as expected
    err := vc.SetTime(0, 6)
    ensureNoError(t, err, "SetTime returned an error: ")
    err = vc.SetTime(1, 4)
    ensureNoError(t, err, "SetTime returned an error: ")
    err = vc.SetTime(2, 0)
    ensureNoError(t, err, "SetTime returned an error: ")
    assertVCsEqual(t, vc, VectorClock{6, 4, 0, 2})

    // Ensure Set returns error when trying to
    // set time less than what is already stored
    err = vc.SetTime(1, 3)
    if err == nil {
        t.Fatal("SetTime did not return an error when rewinding time.")
    }
    assertVCsEqual(t, vc, VectorClock{6, 4, 0, 2})

    // Ensure LessThan works as expected
    wrongSize := VectorClock{0, 0, 0}
    greater := VectorClock{6, 5, 0, 2}
    equal := VectorClock{6, 4, 0, 2}
    less := VectorClock{6, 3, 0, 2}

    assert(t, !wrongSize.LessThan(vc), "LessThan returned true for VC of " +
        "different size")
    assert(t, !greater.LessThan(vc), "LessThan returned true for greater VC")
    assert(t, !equal.LessThan(vc), "LessThan returned true for equal VC")
    assert(t, less.LessThan(vc), "LessThan returned false for lesser VC")

    // Ensure Max works as expected
    other := VectorClock{5, 5, 2, 2}
    vc.Max(other)
    assertVCsEqual(t, vc, VectorClock{6, 5, 2, 2})
    // Ensure other wasn't affected
    assertVCsEqual(t, other, VectorClock{5, 5, 2, 2})
}

/*****************************
 *    BAYOU SERVER TESTS     *
 *****************************/

/* Fails provided test if Bayou Logs do not have equal content *
 * Ensures order of entries are the same is checkOrder is true */
func assertLogsEqual(t *testing.T, log []LogEntry, exp []LogEntry,
        checkOrder bool) {
    failMsg := "Expected Log: " + logToString(exp) + "\nReceived: " +
            logToString(log)

    assertEqual(t, len(log), len(exp), failMsg)
    if checkOrder {
        for idx, _ := range log {
            assert(t, entriesAreEqual(log[idx], exp[idx], true), failMsg)
        }
    } else {
        logCopy := make([]LogEntry, len(log))
        copy(logCopy, log)
        for _, expEntry := range exp {
            for idx, logEntry := range logCopy {
                if entriesAreEqual(expEntry, logEntry, false) {
                    logCopy = append(log[:idx], log[idx+1:]...)
                    break
                }
            }
            t.Fatal(failMsg)
        }
    }
}

/* Fails provided test if provided *
 * Room lists are not identical    */
func assertRoomListsEqual(t *testing.T, rooms []Room, exp []Room,
        prefix string) {
    roomStr := ""
    expStr := ""
    for _, entry := range rooms {
        roomStr = roomStr + entry.String() + "\n"
    }
    for _, entry := range exp {
        expStr = expStr + entry.String() + "\n"
    }
    failMsg := prefix + ":\n\nExepected Rooms:\n" + expStr +
            "\nReceived:\n" + roomStr

    assertEqual(t, len(rooms), len(exp), failMsg)
    for idx, _ := range rooms {
        assert(t, roomsAreEqual(rooms[idx], exp[idx]), failMsg)
    }
}

/* Fails provided test if database contents *
 * do not match the provided Room list      *
 * Acquires provided lock before reading    */
func assertDBContentsEqual(t *testing.T, lock *sync.Mutex,
        db *BayouDB, exp []Room) {
    lock.Lock()
    defer lock.Unlock()
    result := db.Read(getReadAllQuery())
    rooms := deserializeRooms(result)
    assertRoomListsEqual(t, rooms, exp, "Database does not contain " +
        "expected contents")
}

/* Kills each of the provided servers */
func cleanupServers(servers []*BayouServer) {
    for _, server := range servers {
        server.Kill()
        server.commitDB.Close()
        server.fullDB.Close()
        DeletePersist(server.id)
    }
}

/* Closes each of the provided RPC clients */
func cleanupRPCClients(clients []*rpc.Client) {
    for _, client := range clients {
        client.Close()
    }
}

/* Creates a network of Bayou servers and RPC clients *
 * A server is started for each provided server port,  *
 * and a an RPC client for each provided client port   */
func createNetwork(testName string, serverPorts []int,
        clientPorts []int) ([]*BayouServer, []*rpc.Client) {
    serverList := make([]*BayouServer, len(serverPorts))
    rpcClients := make([]*rpc.Client, len(clientPorts))
    for i, port := range serverPorts {
        id := fmt.Sprintf("%d", i)
        commitDB := getDB(testName + "_" + id + "_commit.db", true)
        fullDB := getDB(testName + "_" + id + "_full.db", true)
        serverList[i] = NewBayouServer(i, rpcClients, commitDB, fullDB, port)
    }
    for i, port := range clientPorts {
        rpcClients[i] = startRPCClient(port)
    }
    return serverList, rpcClients
}

/* Shuts down and cleans up the provided network */
func removeNetwork(servers []*BayouServer, clients []*rpc.Client) {
    cleanupRPCClients(clients)
    cleanupServers(servers)
}

/* Starts inter-server communication on the provided network */
func startNetworkComm(servers []*BayouServer) {
    for _, server := range servers {
        server.Start()
    }
}

/* Tests server RPC functionality */
func TestUnitServerRPC(t *testing.T) {
    numClients := 10
    port := 1111
    otherPort := 1112

    serverPorts := []int{port, otherPort}
    clientPorts := make([]int, numClients)
    for i := 0; i < numClients; i++ {
        clientPorts[i] = port
    }
    clientPorts[1] = otherPort

    servers, clients := createNetwork("test_rpc", serverPorts, clientPorts)
    defer cleanupRPCClients(clients)

    server := servers[0]
    otherServer := servers[1]
    defer server.Kill()

    // Test a single RPC
    pingArgs := &PingArgs{2}
    var pingReply PingReply
    err := clients[server.id].Call("BayouServer.Ping", pingArgs, &pingReply)
    ensureNoError(t, err, "Single Ping RPC failed: ")
    assert(t, pingReply.Alive, "Single Ping RPC failed.")

    var wg sync.WaitGroup
    wg.Add(numClients)

    argArr := make([]PingArgs, numClients)
    replyArr := make([]PingReply, numClients)

    // Test several RPC calls at once
    for i := 0; i < numClients; i++ {
        go func(id int) {
            // debugf("Client #%d sending ping!", id)
            argArr[id].SenderID = id
            newErr := clients[id].Call("BayouServer.Ping",
                    &argArr[id], &replyArr[id])
            ensureNoError(t, newErr, "Concurrent Ping RPC Failed: ")
            assert(t, replyArr[id].Alive, "Concurrent Ping RPC failed.")
            wg.Done()
        } (i)
    }
    wg.Wait()

    // Test inter-server RPC
    success := server.SendPing(otherServer.id)
    assert(t, success, "Inter-server Ping RPC failed.")
    success = otherServer.SendPing(server.id)
    assert(t, success, "Inter-server Ping RPC failed.")

    // Ensure RPC to Killed server fails
    otherServer.Kill()
    success = server.SendPing(otherServer.id)
    assert(t, !success, "Ping to Killed server suceeded.")
}

/* Tests server Read and Write functions */
func TestUnitServerReadWrite(t *testing.T) {
    numClients := 10
    port := 1113

    serverPorts := []int{port}
    clientPorts := make([]int, numClients)
    for i := 0; i < numClients; i++ {
        clientPorts[i] = port
    }

    servers, clients := createNetwork("test_read_write",
            serverPorts, clientPorts)
    server := servers[0]
    defer removeNetwork(servers, clients)

    // PART 1: TESTING WRITE RPCs

    room := Room{"RW0", createDate(0, 0), createDate(0, 1)}
    rooms := []Room{room}

    query := getInsertQuery(room)
    undo := getDeleteQuery(room)
    check := getBoolQuery(true)
    merge := getBoolQuery(false)

    vclock := NewVectorClock(numClients)
    vclock.Inc(server.id)
    writeEntry := NewLogEntry(0, vclock, query, check, merge)
    undoEntry := NewLogEntry(0, vclock, undo, getBoolQuery(true),
            getBoolQuery(false))

    // Test a single uncommitted write
    writeArgs := &WriteArgs{0, query, undo, check, merge}
    var writeReply WriteReply
    err := clients[server.id].Call("BayouServer.Write", writeArgs, &writeReply)
    ensureNoError(t, err, "Single Write RPC failed: ")

    assert(t, !writeReply.HasConflict, "Write falsely returned conflict.")
    assert(t, writeReply.WasResolved, "Write was not resolved.")
    assert(t, len(server.CommitLog) == 0, "Uncommitted write changed " +
            "commit log.")
    assert(t, len(server.ErrorLog) == 0, "Write was falsely written " +
            "to error log.")
    assertLogsEqual(t, server.TentativeLog, []LogEntry{writeEntry}, true)
    assertLogsEqual(t, server.UndoLog, []LogEntry{undoEntry}, true)
    assertDBContentsEqual(t, server.logLock, server.commitDB, []Room{})
    assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)

    // Test a conflicting, uncomitted write
    room = Room{"RW1", createDate(1, 0), createDate(1, 1)}
    query = getInsertQuery(room)
    undo = getDeleteQuery(room)
    check = getBoolQuery(false)
    merge = getBoolQuery(true)
    vclock.Inc(server.id)
    writeEntry2 := NewLogEntry(1, vclock, query, check, merge)
    undoEntry2 := NewLogEntry(1, vclock, undo, getBoolQuery(true),
            getBoolQuery(false))

    writeArgs = &WriteArgs{1, query, undo, check, merge}
    writeReply = WriteReply{}
    err = clients[server.id].Call("BayouServer.Write", writeArgs, &writeReply)
    ensureNoError(t, err, "Conflicting Write RPC failed: ")

    assert(t, writeReply.HasConflict, "Write failed to return conflict.")
    assert(t, writeReply.WasResolved, "Write was not resolved.")
    assert(t, len(server.CommitLog) == 0, "Uncommitted write changed " +
            "commit log.")
    assert(t, len(server.ErrorLog) == 0, "Write was falsely written " +
            "to error log.")
    assertLogsEqual(t, server.TentativeLog,
            []LogEntry{writeEntry, writeEntry2}, true)
    assertLogsEqual(t, server.UndoLog,
            []LogEntry{undoEntry, undoEntry2}, true)
    assertDBContentsEqual(t, server.logLock, server.commitDB, []Room{})
    // Note: DB does not change from last test because
    // write conflicts and merge query does not insert anything
    assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)

    // Test a conflicting, unresolvable uncomitted write
    room = Room{"RW2", createDate(2, 0), createDate(2, 1)}
    query = getInsertQuery(room)
    undo = getDeleteQuery(room)
    merge = getBoolQuery(false)
    vclock.Inc(server.id)
    writeEntry3 := NewLogEntry(2, vclock, query, check, merge)
    undoEntry3 := NewLogEntry(2, vclock, undo, getBoolQuery(true),
            getBoolQuery(false))

    writeArgs = &WriteArgs{2, query, undo, check, merge}
    writeReply = WriteReply{}
    err = clients[server.id].Call("BayouServer.Write", writeArgs, &writeReply)
    ensureNoError(t, err, "Unresolveable Write RPC failed: ")

    assert(t, writeReply.HasConflict, "Write failed to return conflict.")
    assert(t, !writeReply.WasResolved, "Write was falsely resolved.")
    assert(t, len(server.CommitLog) == 0, "Uncommitted write changed " +
            "commit log.")
    assertLogsEqual(t, server.ErrorLog, []LogEntry{writeEntry3}, true)
    // Note: In our implementation, even unresolvable writes are
    // added to the write logs, they are just also added to the error log
    assertLogsEqual(t, server.TentativeLog,
            []LogEntry{writeEntry, writeEntry2, writeEntry3}, true)
    assertLogsEqual(t, server.UndoLog,
            []LogEntry{undoEntry, undoEntry2, undoEntry3}, true)
    assertDBContentsEqual(t, server.logLock, server.commitDB, []Room{})
    // Note: As explained above, the DB should not change
    assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)

    // Test a committed write
    room = Room{"RW3", createDate(3, 0), createDate(3, 1)}
    rooms = append(rooms, room)
    query = getInsertQuery(room)
    undo = getDeleteQuery(room)
    check = getBoolQuery(true)
    vclock = NewVectorClock(numClients)
    vclock.Inc(server.id)
    writeEntry4 := NewLogEntry(3, vclock, query, check, merge)

    server.IsPrimary = true
    writeArgs = &WriteArgs{3, query, undo, check, merge}
    writeReply = WriteReply{}
    err = clients[server.id].Call("BayouServer.Write", writeArgs, &writeReply)
    ensureNoError(t, err, "Comitted Write RPC failed: ")

    assert(t, !writeReply.HasConflict, "Write falsely returned conflict.")
    assert(t, writeReply.WasResolved, "Write was falsely resolved.")
    assertLogsEqual(t, server.CommitLog, []LogEntry{writeEntry4}, true)
    assertLogsEqual(t, server.ErrorLog, []LogEntry{writeEntry3}, true)
    assertLogsEqual(t, server.TentativeLog,
            []LogEntry{writeEntry, writeEntry2, writeEntry3}, true)
    assertLogsEqual(t, server.UndoLog,
            []LogEntry{undoEntry, undoEntry2, undoEntry3}, true)
    assertDBContentsEqual(t, server.logLock, server.commitDB, []Room{room})
    // Note: committed writes are added to committed and full DBs,
    // which is why expect the room to appear in the ful DB
    assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)

    // PART 2: TESTING READ RPCs

    // Test a no-op read query
    query = getBoolQuery(true)
    readArgs := &ReadArgs{query, true}
    var readReply ReadReply
    err = clients[server.id].Call("BayouServer.Read", readArgs, &readReply)
    ensureNoError(t, err, "No-op Read RPC failed: ")

    assertEqual(t, len(readReply.Data), 1, "No-op query returned map " +
            "of wrong size")
    value, hasKey := readReply.Data[0]["1"]
    assert(t, hasKey, "No-op query returned map with wrong keys")
    assertEqual(t, value, int64(1), "No-op query returned map with wrong value")

    // Test a read-all query from full DB
    query = getReadAllQuery()
    readArgs = &ReadArgs{query, false}
    readReply = ReadReply{}
    err = clients[server.id].Call("BayouServer.Read", readArgs, &readReply)
    ensureNoError(t, err, "Read all RPC failed: ")
    readRooms := deserializeRooms(readReply.Data)
    assertRoomListsEqual(t, readRooms, rooms, "Incorrect Read All result: ")

    // Test a specific read query from full DB
    query = getReadQuery(rooms[0])
    readArgs = &ReadArgs{query, false}
    readReply = ReadReply{}
    err = clients[server.id].Call("BayouServer.Read", readArgs, &readReply)
    ensureNoError(t, err, "Specific Read RPC failed: ")
    readRooms = deserializeRooms(readReply.Data)
    assertRoomListsEqual(t, readRooms, []Room{rooms[0]}, "Incorrect " +
            "specific Read result: ")

    // Test a read query from commit DB
    query = getReadAllQuery()
    readArgs = &ReadArgs{query, true}
    readReply = ReadReply{}
    err = clients[server.id].Call("BayouServer.Read", readArgs, &readReply)
    ensureNoError(t, err, "Read all committed RPC failed: ")
    readRooms = deserializeRooms(readReply.Data)
    assertRoomListsEqual(t, readRooms, []Room{room}, "Incorrect " +
            "Read all comitted result: ")

    // Test that query for non-existent item returns nothing
    query = getReadQuery(rooms[0])
    readArgs = &ReadArgs{query, true}
    readReply = ReadReply{}
    err = clients[server.id].Call("BayouServer.Read", readArgs, &readReply)
    ensureNoError(t, err, "Read non-existent RPC failed: ")
    assertEqual(t, len(readReply.Data), 0, "Read of non-existent item " +
            "returned non-empty result")

    // PART 3: CONCURRENT READS / WRITES

    var wg sync.WaitGroup
    wg.Add(numClients)

    writeArgArr := make([]WriteArgs, numClients)
    writeReplyArr := make([]WriteReply, numClients)

    // Perform Concurrent Write RPC calls
    for i := 0; i < numClients; i++ {
        go func(id int) {
            // debugf("Client #%d sending write!", id)
            roomName := fmt.Sprintf("ZRW%d", id)
            croom := Room{roomName, createDate(id, 0), createDate(id, 1)}
            cquery := getInsertQuery(croom)
            cundo := getDeleteQuery(croom)
            writeArgArr[id] = WriteArgs{10+id, cquery, cundo, check, merge}
            cerr := clients[server.id].Call("BayouServer.Write",
                    &writeArgArr[id], &writeReplyArr[id])
            ensureNoError(t, cerr, "Concurrent Write RPC failed: ")
            wg.Done()
        } (i)
    }
    wg.Wait()

    // Update rooms array to hold contents of previous writes
    for i := 0; i < numClients; i++ {
        newroom := Room{fmt.Sprintf("ZRW%d", i), createDate(i, 0),
                createDate(i, 1)}
        rooms = append(rooms, newroom)
    }
    assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)

    query = getReadAllQuery()
    readArgArr := make([]ReadArgs, numClients)
    readReplyArr := make([]ReadReply, numClients)

    // Perform Concurrent Read RPC calls
    // and ensure results are correct
    wg.Add(numClients)
    for i := 0; i < numClients; i++ {
        go func(id int) {
            // debugf("Client #%d sending read!", id)
            readArgArr[id] = ReadArgs{query, false}
            rerr := clients[server.id].Call("BayouServer.Read",
                    &readArgArr[id], &readReplyArr[id])
            ensureNoError(t, rerr, "Concurrent Read RPC failed: ")
            // Ensure results are correct
            crooms := deserializeRooms(readReplyArr[id].Data)
            assertRoomListsEqual(t, crooms, rooms, "Concurrent R/W " +
                    "returned incorrect result: ")
            wg.Done()
        } (i)
    }
    wg.Wait()
 }

/* Tests server Anti-Entropy communication */
func TestUnitServerAntiEntropy(t *testing.T) {
    numClients := 5
    numWrites := 10
    startPort := 1114

    // Note: numWrites should be <= 10, because due to the way
    // assertDBContentsEqual works, log entries of "10" and above
    // appear in an unexpected order and fail. This is a limitation
    // of the testing environment, not the implementation

    serverPorts := make([]int, numClients)
    clientPorts := make([]int, numClients)
    for i := 0; i < numClients; i++ {
        serverPorts[i] = startPort + i
        clientPorts[i] = startPort + i
    }

    servers, clients := createNetwork("test_antientropy",
            serverPorts, clientPorts)
    defer removeNetwork(servers, clients)
    startNetworkComm(servers)

    debugf("TESTING ANTI-ENTROPY: DELAYS MAY FOLLOW")

    startID := randomIntn(numClients)
    rooms := []Room{}
    check := getBoolQuery(true)
    merge := getBoolQuery(false)

    // Perform a series of writes on random servers
    for i := 0; i < numWrites; i++ {
        room := Room{fmt.Sprintf("AEU%d", i), createDate(i, 0),
                createDate(i, 1)}
        rooms = append(rooms, room)
        query := getInsertQuery(room)
        undo := getDeleteQuery(room)
        writeArgs := &WriteArgs{i, query, undo, check, merge}
        var writeReply WriteReply
        serverID := (startID + i) % numClients
        err := clients[serverID].Call("BayouServer.Write",
                writeArgs, &writeReply)
        ensureNoError(t, err, "Write RPC failed: ")
        assert(t, !writeReply.HasConflict, "Write falesly returned conflict.")
        assert(t, writeReply.WasResolved, "Write was not resolved.")
    }

    // Wait for anti-entropy to occur
    sleepTime := ANTI_ENTROPY_TIMEOUT_MIN * numClients * 2
    sleep(sleepTime, true)

    // Ensure all servers have received all writes
    for _, server := range servers {
        assert(t, len(server.CommitLog) == 0, "Uncomitted writes changed " +
                "commit log")
        assertDBContentsEqual(t, server.logLock, server.fullDB, rooms)
    }
}

/* Tests server persistence and recovery */
func TestUnitServerPersist(t *testing.T) {
    servers, clients := createBayouNetwork("persistTest", 1)
    clients[0].ClaimRoom("Frist",  1, 1)
    clients[0].ClaimRoom("Jadwin", 1, 1)

    log1 := servers[0].TentativeLog

    // kill them all (muahaha)
    clients[0].Kill()
    servers[0].Kill()
    servers[0].commitDB.Close()
    servers[0].fullDB.Close()

    // Check that Persist worked
    servers, clients = createBayouNetwork("persistTest", 1)
    defer removeBayouNetwork(servers, clients)
    log2 := servers[0].TentativeLog
    assertLogsEqual(t, log1, log2, true)
}

/******************************
 *    BAYOU NETWORK TESTS     *
 ******************************/

/* Creates a network of Bayou Server-Client clusters */
func createBayouNetwork(testName string, numClusters int) ([]*BayouServer,
        []*BayouClient) {
    ports := make([]int, numClusters)
    for i := 0; i < numClusters; i++ {
        ports[i] = 1111 + i
    }
    clientList := make([]*BayouClient, numClusters)
    serverList, rpcClients := createNetwork(testName, ports, ports)
    for i, rpcClient := range rpcClients {
        clientList[i] = NewBayouClient(i, rpcClient)
    }
    return serverList, clientList
}

/* Shuts down and cleans up the provided network */
func removeBayouNetwork(servers []*BayouServer, clients []*BayouClient) {
    for _, client := range clients {
        client.Kill()
    }
    cleanupServers(servers)
}

/* Tests client functionality */
func TestUnitClient(t *testing.T) {
    servers, clients := createBayouNetwork("test_client", 1)
    defer removeBayouNetwork(servers, clients)

    // Test non-conflicting write
    clients[0].ClaimRoom("Frist", 1, 1)

    // Check that room is claimed
    room := clients[0].CheckRoom("Frist", 1, 1, false)
    assert(t, room.Name == "Frist", "Room is broken")

    // Check that other room is not claimed
    room = clients[0].CheckRoom("Frist", 2, 1, false)
    assert(t, room.Name == "-1", "Room is broken")
}
