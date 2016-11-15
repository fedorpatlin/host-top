package main

import "net/http"
import "fmt"
import "log"
import "sync"
import "sort"
import "time"

// глобальные переменные. Хеш-таблица хост-запросы,
// порог числа запросов в секунду и тикер
var th *hostMap
var ticker *time.Ticker
var rpsTreshold = 100

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
	th = new(hostMap)
	ticker = time.NewTicker(1 * time.Second)
	go dumpAll()
	th.counters = make(map[string]int64)
	http.HandleFunc("/", hostExtractor)
	log.Fatal(http.ListenAndServe(":80", nil))
}

func hostExtractor(w http.ResponseWriter, req *http.Request) {
	th.Inc(req.Host)
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
