package cluster

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/duanhf2012/origin/service"

	"github.com/duanhf2012/origin/rpc"
)

//https://github.com/rocket049/rpc2d/blob/master/rpcnode.go
//http://daizuozhuo.github.io/golang-rpc-practice/

type RpcClient struct {
	nodeid     int
	pclient    *rpc.Client
	serverAddr string
	isConnect  bool
}

type CCluster struct {
	port       int
	cfg        *ClusterConfig
	nodeclient map[int]*RpcClient

	reader net.Conn
	writer net.Conn

	LocalRpcClient *rpc.Client
}

func (slf *CCluster) ReadNodeInfo(nodeid int) error {
	//连接Server结点
	var err error

	slf.cfg, err = ReadCfg("./config/cluster.json", nodeid)
	if err != nil {
		fmt.Printf("%v", err)
		return nil
	}

	return nil
}

func (slf *CCluster) GetClusterClient(id int) *rpc.Client {
	v, ok := slf.nodeclient[id]
	if ok == false {
		return nil
	}

	return v.pclient
}

func (slf *CCluster) GetClusterNode(strNodeName string) *CNodeCfg {
	for _, value := range slf.cfg.NodeList {
		if value.NodeName == strNodeName {
			return &value
		}
	}

	return nil
}

func (slf *CCluster) GetBindUrl() (string, error) {
	return slf.cfg.currentNode.ServerAddr, nil
}

type CTestData struct {
	Bbbb int64
	Cccc int
	Ddd  string
}

func (slf *CCluster) AcceptRpc(tpcListen *net.TCPListener) error {
	slf.reader, slf.writer = net.Pipe()
	go rpc.ServeConn(slf.reader)
	slf.LocalRpcClient = rpc.NewClient(slf.writer)

	for {
		conn, err := tpcListen.Accept()
		if err != nil {
			fmt.Print(err)
			return err
		}

		//使用goroutine单独处理rpc连接请求
		go rpc.ServeConn(conn)
	}

	return nil
}

func (slf *CCluster) ListenService() error {

	bindStr, err := slf.GetBindUrl()
	if err != nil {
		return err
	}

	tcpaddr, err := net.ResolveTCPAddr("tcp4", bindStr)
	if err != nil {
		return err
	}

	tcplisten, err2 := net.ListenTCP("tcp", tcpaddr)
	if err2 != nil {
		return err2
	}

	go slf.AcceptRpc(tcplisten)
	return nil
}

type CPing struct {
	TimeStamp int64
}

type CPong struct {
	TimeStamp int64
}

func (slf *CPing) Ping(ping *CPing, pong *CPong) error {
	pong.TimeStamp = ping.TimeStamp
	return nil
}

func (slf *CCluster) ConnService() error {
	ping := CPing{0}
	pong := CPong{0}
	fmt.Println(rpc.RegisterName("CPing", &ping))

	//连接集群服务器
	for _, nodeList := range slf.cfg.mapClusterNodeService {
		for _, node := range nodeList {
			slf.nodeclient[node.NodeID] = &RpcClient{node.NodeID, nil, node.ServerAddr, false}
		}
	}

	for {
		for _, rpcClient := range slf.nodeclient {

			//
			if rpcClient.isConnect == true {
				ping.TimeStamp = 0
				err := rpcClient.pclient.Call("CPing.Ping", &ping, &pong)
				if err != nil {
					rpcClient.pclient.Close()
					rpcClient.pclient = nil
					rpcClient.isConnect = false
					continue
				}
				continue
			}

			if rpcClient.pclient != nil {
				rpcClient.pclient.Close()
				rpcClient.pclient = nil
			}

			client, err := rpc.Dial("tcp", rpcClient.serverAddr)
			if err != nil {
				log.Println(err)
				continue
			}

			v, _ := slf.nodeclient[rpcClient.nodeid]
			v.pclient = client
			v.isConnect = true
		}

		time.Sleep(time.Second * 2)
	}

	return nil
}

func (slf *CCluster) Init() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("param error not find NodeId=number")
	}

	parts := strings.Split(os.Args[1], "=")
	if len(parts) < 2 {
		return fmt.Errorf("param error not find NodeId=number")
	}
	if parts[0] != "NodeId" {
		return fmt.Errorf("param error not find NodeId=number")
	}

	slf.nodeclient = make(map[int]*RpcClient)

	//读取配置
	ret, err := strconv.Atoi(parts[1])
	if err != nil {
		return err
	}

	return slf.ReadNodeInfo(ret)
}

func (slf *CCluster) Start() error {

	service.InstanceServiceMgr().FetchService(slf.OnFetchService)

	//监听服务
	slf.ListenService()

	//集群
	go slf.ConnService()

	return nil
}

//Node.servicename.methodname
//servicename.methodname
//_servicename.methodname
func (slf *CCluster) Call(NodeServiceMethod string, args interface{}, reply interface{}) error {
	var callServiceName string
	nodeidList := slf.GetNodeList(NodeServiceMethod, &callServiceName)
	if len(nodeidList) > 1 {
		return fmt.Errorf("Call: %s find %d nodes.", NodeServiceMethod, len(nodeidList))
	}

	for _, nodeid := range nodeidList {
		if nodeid == slf.GetCurrentNodeId() {
			return slf.LocalRpcClient.Call(callServiceName, args, reply)
		} else {

			pclient := slf.GetClusterClient(nodeid)

			if pclient == nil {
				return fmt.Errorf("Call: NodeId %d is not find.", nodeid)
			}
			err := pclient.Call(callServiceName, args, reply)
			return err
		}
	}

	return fmt.Errorf("Call: %s fail.", NodeServiceMethod)
}

func (slf *CCluster) GetNodeList(NodeServiceMethod string, rpcServerMethod *string) []int {
	var nodename string
	var servicename string
	var methodname string
	var nodeidList []int

	parts := strings.Split(NodeServiceMethod, ".")
	if len(parts) == 2 {
		servicename = parts[0]
		methodname = parts[1]
	} else if len(parts) == 3 {
		nodename = parts[0]
		servicename = parts[1]
		methodname = parts[2]
	} else {
		return nodeidList
	}

	if nodename == "" {
		nodeidList = make([]int, 0)
		if servicename[:1] == "_" {
			servicename = servicename[1:]
			nodeidList = append(nodeidList, slf.GetCurrentNodeId())
		} else {
			nodeidList = slf.cfg.GetIdByService(servicename)
		}
	} else {
		nodeidList = slf.GetIdByNodeService(nodename, servicename)
	}

	*rpcServerMethod = servicename + "." + methodname
	return nodeidList
}

func (slf *CCluster) Go(NodeServiceMethod string, args interface{}) error {
	var callServiceName string
	nodeidList := slf.GetNodeList(NodeServiceMethod, &callServiceName)
	if len(nodeidList) > 1 {
		return fmt.Errorf("Call: %s find %d nodes.", NodeServiceMethod, len(nodeidList))
	}

	for _, nodeid := range nodeidList {
		if nodeid == slf.GetCurrentNodeId() {
			slf.LocalRpcClient.Go(callServiceName, args, nil, nil)
			return nil
		} else {

			pclient := slf.GetClusterClient(nodeid)

			if pclient == nil {
				return fmt.Errorf("Call: NodeId %d is not find.", nodeid)
			}
			pclient.Go(callServiceName, args, nil, nil)
			return nil
		}
	}

	return fmt.Errorf("Call: %s fail.", NodeServiceMethod)
}
func (slf *CCluster) CallNode(nodeid int, servicemethod string, args interface{}, reply interface{}) error {
	pclient := slf.GetClusterClient(nodeid)
	if pclient == nil {
		return fmt.Errorf("Call: NodeId %d is not find.", nodeid)
	}

	err := pclient.Call(servicemethod, args, reply)
	return err

}

func (slf *CCluster) GoNode(nodeid int, args interface{}, servicemethod string) error {
	pclient := slf.GetClusterClient(nodeid)
	if pclient == nil {
		return fmt.Errorf("Call: NodeId %d is not find.", nodeid)
	}

	replyCall := pclient.Go(servicemethod, args, nil, nil)
	//ret := <-replyCall.Done
	if replyCall.Error != nil {
		fmt.Print(replyCall.Error)
	}

	//fmt.Print(ret)
	return nil

}

func (ws *CCluster) OnFetchService(iservice service.IService) error {
	rpc.RegisterName(iservice.GetServiceName(), iservice)
	return nil
}

//向远程服务器调用
//Node.servicename.methodname
//servicename.methodname
func Call(NodeServiceMethod string, args interface{}, reply interface{}) error {
	return InstanceClusterMgr().Call(NodeServiceMethod, args, reply)
}

func Go(NodeServiceMethod string, args interface{}) error {
	return InstanceClusterMgr().Go(NodeServiceMethod, args)
}

func CallNode(NodeId int, servicemethod string, args interface{}, reply interface{}) error {
	return InstanceClusterMgr().CallNode(NodeId, servicemethod, args, reply)
}

func GoNode(NodeId int, servicemethod string, args interface{}) error {
	return InstanceClusterMgr().GoNode(NodeId, args, servicemethod)
}

func CastCall(NodeServiceMethod string, args interface{}, reply []interface{}) error {
	return InstanceClusterMgr().Call(NodeServiceMethod, args, reply)
}

var _self *CCluster

func InstanceClusterMgr() *CCluster {
	if _self == nil {
		_self = new(CCluster)
		return _self
	}
	return _self
}

func (slf *CCluster) GetIdByNodeService(NodeName string, serviceName string) []int {
	return slf.cfg.GetIdByNodeService(NodeName, serviceName)
}

func (slf *CCluster) HasLocalService(serviceName string) bool {
	return slf.cfg.HasLocalService(serviceName)
}

func (slf *CCluster) GetCurrentNodeId() int {
	return slf.cfg.currentNode.NodeID
}