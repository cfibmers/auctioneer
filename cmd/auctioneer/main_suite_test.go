package main_test

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/cloudfoundry-incubator/auctioneer"
	"github.com/cloudfoundry-incubator/bbs"
	bbstestrunner "github.com/cloudfoundry-incubator/bbs/cmd/bbs/testrunner"
	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/bbs/test_helpers"
	"github.com/cloudfoundry-incubator/bbs/test_helpers/sqlrunner"
	"github.com/cloudfoundry-incubator/consuladapter"
	"github.com/cloudfoundry-incubator/consuladapter/consulrunner"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"

	"testing"
	"time"
)

var (
	auctioneerProcess ifrit.Process

	auctioneerPath string

	dotNetStack           = "dot-net"
	dotNetRootFSURL       = models.PreloadedRootFS(dotNetStack)
	linuxStack            = "linux"
	linuxRootFSURL        = models.PreloadedRootFS(linuxStack)
	dotNetCell, linuxCell *FakeCell

	auctioneerServerPort int
	auctioneerAddress    string
	runner               *ginkgomon.Runner

	etcdPort   int
	etcdRunner *etcdstorerunner.ETCDClusterRunner

	consulRunner *consulrunner.ClusterRunner
	consulClient consuladapter.Client

	auctioneerClient auctioneer.Client

	bbsArgs    bbstestrunner.Args
	bbsBinPath string
	bbsURL     *url.URL
	bbsRunner  *ginkgomon.Runner
	bbsProcess ifrit.Process
	bbsClient  bbs.InternalClient

	sqlProcess ifrit.Process
	sqlRunner  sqlrunner.SQLRunner

	logger lager.Logger
)

func TestAuctioneer(t *testing.T) {
	// these integration tests can take a bit, especially under load;
	// 1 second is too harsh
	SetDefaultEventuallyTimeout(10 * time.Second)

	RegisterFailHandler(Fail)
	RunSpecs(t, "Auctioneer Cmd Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	bbsConfig, err := gexec.Build("github.com/cloudfoundry-incubator/bbs/cmd/bbs", "-race")
	Expect(err).NotTo(HaveOccurred())

	compiledAuctioneerPath, err := gexec.Build("github.com/cloudfoundry-incubator/auctioneer/cmd/auctioneer", "-race")
	Expect(err).NotTo(HaveOccurred())
	return []byte(strings.Join([]string{compiledAuctioneerPath, bbsConfig}, ","))
}, func(pathsByte []byte) {
	path := string(pathsByte)
	compiledAuctioneerPath := strings.Split(path, ",")[0]
	bbsBinPath = strings.Split(path, ",")[1]

	bbsBinPath = strings.Split(path, ",")[1]
	auctioneerPath = string(compiledAuctioneerPath)

	auctioneerServerPort = 1800 + GinkgoParallelNode()
	auctioneerAddress = fmt.Sprintf("http://127.0.0.1:%d", auctioneerServerPort)

	etcdPort = 5001 + GinkgoParallelNode()
	etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1, nil)

	if test_helpers.UseSQL() {
		dbName := fmt.Sprintf("diego_%d", GinkgoParallelNode())
		sqlRunner = test_helpers.NewSQLRunner(dbName)
		sqlProcess = ginkgomon.Invoke(sqlRunner)
	}

	consulRunner = consulrunner.NewClusterRunner(
		9001+config.GinkgoConfig.ParallelNode*consulrunner.PortOffsetLength,
		1,
		"http",
	)

	auctioneerClient = auctioneer.NewClient(auctioneerAddress)

	logger = lagertest.NewTestLogger("test")

	consulRunner.Start()
	consulRunner.WaitUntilReady()

	bbsPort := 13000 + GinkgoParallelNode()*2
	healthPort := bbsPort + 1
	bbsAddress := fmt.Sprintf("127.0.0.1:%d", bbsPort)
	healthAddress := fmt.Sprintf("127.0.0.1:%d", healthPort)

	bbsURL = &url.URL{
		Scheme: "http",
		Host:   bbsAddress,
	}

	bbsClient = bbs.NewClient(bbsURL.String())

	etcdUrl := fmt.Sprintf("http://127.0.0.1:%d", etcdPort)
	bbsArgs = bbstestrunner.Args{
		Address:           bbsAddress,
		AdvertiseURL:      bbsURL.String(),
		AuctioneerAddress: auctioneerAddress,
		EtcdCluster:       etcdUrl,
		ConsulCluster:     consulRunner.ConsulCluster(),
		HealthAddress:     healthAddress,

		EncryptionKeys: []string{"label:key"},
		ActiveKeyLabel: "label",
	}

	if test_helpers.UseSQL() {
		bbsArgs.DatabaseDriver = sqlRunner.DriverName()
		bbsArgs.DatabaseConnectionString = sqlRunner.ConnectionString()
	}
})

var _ = BeforeEach(func() {
	consulRunner.Reset()
	etcdRunner.Start()

	bbsRunner = bbstestrunner.New(bbsBinPath, bbsArgs)
	bbsProcess = ginkgomon.Invoke(bbsRunner)

	consulClient = consulRunner.NewClient()

	serviceClient := bbs.NewServiceClient(consulClient, clock.NewClock())

	runner = ginkgomon.New(ginkgomon.Config{
		Name: "auctioneer",
		Command: exec.Command(
			auctioneerPath,
			"-bbsAddress", bbsURL.String(),
			"-listenAddr", fmt.Sprintf("0.0.0.0:%d", auctioneerServerPort),
			"-lockRetryInterval", "1s",
			"-consulCluster", consulRunner.ConsulCluster(),
		),
		StartCheck: "auctioneer.started",
	})

	dotNetCell = SpinUpFakeCell(serviceClient, "dot-net-cell", dotNetStack)
	linuxCell = SpinUpFakeCell(serviceClient, "linux-cell", linuxStack)
})

var _ = AfterEach(func() {
	ginkgomon.Kill(auctioneerProcess)
	etcdRunner.Stop()
	ginkgomon.Kill(bbsProcess)
	dotNetCell.Stop()
	linuxCell.Stop()

	if test_helpers.UseSQL() {
		sqlRunner.Reset()
	}
})

var _ = SynchronizedAfterSuite(func() {
	if etcdRunner != nil {
		etcdRunner.Stop()
	}
	if consulRunner != nil {
		consulRunner.Stop()
	}

	ginkgomon.Kill(sqlProcess)
}, func() {
	gexec.CleanupBuildArtifacts()
})
