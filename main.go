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

func init() {
	runtime.LockOSThread()
}

// глобальные переменные. Хеш-таблица хост-запросы,
// порог числа запросов в секунду и тикер
var th *hostMap
var ticker *time.Ticker
var rpsTreshold = 100
var wg sync.WaitGroup

// хеш-таблица хост - счетчик запросов. Мьютекс для синхронизации доступа.
type hostMap struct {
	counters map[string]int64
	lock     sync.RWMutex
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

func (th *hostMap) ToList() topList {
	var lst = make(topList, len(th.counters))
	var i int
	th.lock.RLock()
	defer th.lock.RUnlock()
	for k, v := range th.counters {
		lst[i] = hostRecord{k, v}
		i++
	}
	return lst
}

type hostRecord struct {
	key   string
	value int64
}

type topList []hostRecord

// Представление хеш-таблицы счетчиков хостов в виде списка,
// сортированного по количеству запросов
func newTopList(th *hostMap) topList {
	lst := th.ToList()
	sort.Sort(sort.Reverse(lst))
	return lst
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
	runtime.LockOSThread()
	ticker = time.NewTicker(1 * time.Second)
	th = new(hostMap)
	th.counters = make(map[string]int64)
	pl := searchNginxWorkers()
	if len(pl) < 1 {
		log.Fatal("Can't find nginx workers. Is nginx up and running?")
	}
	for _, p := range pl {
		go syscallSpy(p, 45)
		wg.Add(1)
	}
	go dumpAll()
	wg.Wait()
}

func attachProcess(pid int) error {

	if err := syscall.PtraceAttach(pid); err != nil {
		return fmt.Errorf("Can't attach to pid %d, error: %s\n", pid, err.Error())
	}
	//	fmt.Printf("Attached to process %d, waiting for syscall\n", pid)
	return nil
}

func detachProcess(pid int) {
	if err := syscall.PtraceDetach(pid); err == nil {
		fmt.Printf("Detached from %d\n", pid)
	} else {
		fmt.Println(err.Error())
	}
}

func syscallSpy(pid int, syscallNr int) {
	// tracer надо привязать к треду, иначе будет ошибка ESRCH в рандомных местах
	runtime.LockOSThread()
	defer wg.Done()
	var out []byte
	if err := attachProcess(pid); err != nil {
		fmt.Println(err.Error())
	} else {
		defer detachProcess(pid)
		for {
			if regsout, err := waitForSyscall(pid, syscallNr); err == nil {
				//			syscall.PtraceGetRegs(pid, &regsout)
				if int(regsout.Rax) > 0 {
					out = make([]byte, regsout.Rdx)
					syscall.PtracePeekData(pid, uintptr(regsout.Rsi), out)
					if h, err := hostExtractor(out); err == nil {
						th.Inc(string(h))
					}
				}
			} else {
				fmt.Errorf(err.Error())
			}
		}
	}
}

func waitForSyscall(pid int, syscallnum int) (syscall.PtraceRegs, error) {
	var wstatus syscall.WaitStatus
	var rusage syscall.Rusage
	var regsout syscall.PtraceRegs
	syscall.PtraceSetOptions(pid, syscall.PTRACE_O_TRACESYSGOOD)
	for {

		if err := syscall.PtraceSyscall(pid, 0); err != nil {
			return regsout, fmt.Errorf("Error PtraceSyscall %s, %d", err.Error())
		}

		if _, err := syscall.Wait4(pid, &wstatus, 0, &rusage); err == nil {

			if wstatus.Stopped() && wstatus.Signal()&0x80 == 0x80 {

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

func hostExtractor(request []byte) (string, error) {
	//	fmt.Println(string(request))
	out := bytes.Split(request, []byte{'\r', '\n'})
	if isRequest(out[0]) {
		for _, h := range out {
			h := bytes.TrimSpace(h)
			if bytes.HasPrefix(h, []byte("Host:")) {
				h := bytes.TrimSpace(bytes.Split(h, []byte{':'})[1])
				return string(h), nil
			}
		}
		return "", fmt.Errorf("No Host header\n")
	}
	return "", fmt.Errorf("WTF is this?")
}

func isRequest(firstString []byte) bool {
	fstr := bytes.TrimSpace(firstString)
	words := bytes.Split(fstr, []byte(" "))
	if len(words) > 2 {
		// в words[0] должен лежать HTTP-метод, но их дохуя и они могут быть добавлены
		// плагинами, разбирать uri тоже желания нет, поэтому, смотрим words[2].
		// words[2] должен содержать версию протокола (HTTP/1.1)
		if bytes.HasPrefix(words[2], []byte("HTTP")) {
			return true
		} else {
			return false
		}
	} else {
		return false
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
		tl := newTopList(th)
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
