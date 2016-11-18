package main

import "bytes"
import "fmt"
import "log"
import "sync"
import "sort"
import "time"
import "os"
import "bufio"
import "runtime"
import "strings"
import "strconv"
import "syscall"

// глобальные переменные. Хеш-таблица хост-запросы,
// порог числа запросов в секунду и тикер
var th *hostMap
var ticker *time.Ticker
var rpsTreshold = 100
var wg sync.WaitGroup

// хеш-таблица хост - счетчик запросов. Мьютекс для синхронизации доступа.
type hostMap struct {
	counters map[string]int64
	lock     sync.Mutex
}

// увеличение на 1 счетчика запросов к хосту
func (th *hostMap) Inc(host string) {
	th.lock.Lock()
	defer th.lock.Unlock()
	if _, ok := th.counters[host]; !ok {
		th.counters[host] = 1
	} else {
		th.counters[host]++
	}
}

type hostRecord struct {
	key   string
	value int64
}

type topList []hostRecord

// Представление хеш-таблицы счетчиков хостов в виде списка,
// сортированного по количеству запросов
func newTopList(hm map[string]int64) topList {
	var tl = make(topList, len(hm))
	i := 0
	for k, v := range hm {
		tl[i] = hostRecord{k, v}
		i++
	}
	sort.Sort(sort.Reverse(tl))
	return tl
}

// Реализация интерфейса sort

func (tl topList) Len() int {
	return len(tl)
}

func (tl topList) Swap(i, j int) {
	tl[i], tl[j] = tl[j], tl[i]
}

func (tl topList) Less(i, j int) bool {
	return tl[i].value < tl[j].value
}

func main() {
	ticker = time.NewTicker(1 * time.Second)
	th = new(hostMap)
	th.counters = make(map[string]int64)
	pl := searchNginxWorkers()
	if len(pl) < 1 {
		log.Fatal("Can't find nginx workers. Is nginx up and running?")
	}
	for _, p := range pl {
		go attachProcess(p)
		wg.Add(1)
	}
	go dumpAll()
	wg.Wait()
}

func attachProcess(pid int) {

	defer wg.Done()
	var out []byte
	// tracer надо привязать к треду, иначе будет ошибка ESRCH в рандомных местах
	runtime.LockOSThread()
	if err := syscall.PtraceAttach(pid); err != nil {
		fmt.Printf("Can't connect to pid %d, error: %s\n", pid, err.Error())
	}
	defer detachProcess(pid)
	//	fmt.Printf("Attached to process %d, waiting for syscall\n", pid)
	for {
		if regsout, err := waitForSyscall(pid, 45); err == nil {
			syscall.PtraceGetRegs(pid, &regsout)
			if int(regsout.Rax) > 0 {
				out = make([]byte, regsout.Rdx)
				syscall.PtracePeekData(pid, uintptr(regsout.Rsi), out)
				hostExtractor(out)
			}
		} else {
			log.Fatal(err.Error())
		}
	}

}

func detachProcess(pid int) {
	if err := syscall.PtraceDetach(pid); err == nil {
		fmt.Printf("Detached from %d\n", pid)
	} else {
		fmt.Println(err.Error())
	}
}

func waitForSyscall(pid int, syscallnum int) (syscall.PtraceRegs, error) {
	var wstatus syscall.WaitStatus
	var rusage syscall.Rusage
	var regsout syscall.PtraceRegs
	syscall.PtraceSetOptions(pid, syscall.PTRACE_O_TRACESYSGOOD)
	for {

		if err := syscall.PtraceSyscall(pid, 0); err != nil {
			return regsout, fmt.Errorf("Error PtraceSyscall %s", err.Error())
		}

		if _, err := syscall.Wait4(pid, &wstatus, 0, &rusage); err == nil {

			if wstatus.Stopped() {

				if err := syscall.PtraceGetRegs(pid, &regsout); err != nil {
					return regsout, fmt.Errorf("Error getregs: %s", err.Error())
				}
				if regsout.Orig_rax == uint64(syscallnum) {
					return regsout, nil
				}
			}
			if wstatus.Exited() {
				return regsout, fmt.Errorf("Process %d exited unexpectedly, exit-code %d, trap cause %s", pid, wstatus.ExitStatus(), wstatus.TrapCause())
			}
		}
	}
}

func searchNginxWorkers() []int {
	var pidlist = make([]int, 0)
	if procdir, err := os.Open("/proc"); err == nil {
		dirs, _ := procdir.Readdir(0)
		for _, d := range dirs {
			if d.IsDir() && (d.Name()[0] > '0' && d.Name()[0] < '9') {
				f, _ := os.Open("/proc/" + d.Name() + "/cmdline")
				reader := bufio.NewReader(f)
				line, _ := reader.ReadString('\n')
				if strings.HasPrefix(line, "nginx: worker process") {
					pid, _ := strconv.ParseInt(d.Name(), 10, 0)
					pidlist = append(pidlist, int(pid))
				}
			}
		}
	} else {
		log.Fatal("Can't open /proc " + err.Error())
	}
	return pidlist

}

func hostExtractor(request []byte) {
	out := bytes.Split(request, []byte{'\r', '\n'})
	for _, h := range out {
		if bytes.HasPrefix(h, []byte("Host:")) {
			h := bytes.TrimSpace(bytes.Split(h, []byte{':'})[1])
			th.Inc(string(h))
			break
		}
	}
}

// Печатать по приходу тикера среднее количество запросов в секунду на хост
func dumpAll() {
	var seconds int64
	fmt.Print("\033c")
	fmt.Printf("RPS\t|Hostname\n")
	fmt.Printf("-------------------------------\n")
	for {
		_ = <-ticker.C
		seconds++
		//clear console
		fmt.Print("\033[3;0H")
		tl := newTopList(th.counters)
		for _, v := range tl {
			rps := v.value / seconds
			if rps < int64(rpsTreshold) {
				fmt.Printf("%d/s\t|%s\n", rps, v.key)
			} else {
				// печатать красным
				fmt.Printf("\033[31;1m%d/s\t|%s\033[0m\n", rps, v.key)
			}
		}
	}
}
