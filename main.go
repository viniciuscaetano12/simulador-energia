// Simulador de poupança energética · Portugal (eletricidade e gás natural)
//
// FICHEIRO ÚNICO: servidor web + motor de cálculo em Go + interface (HTML/CSS/JS) embutida
// + leitura automática de faturas (PDF, foto, Word, Excel, texto).
//
// Executar localmente (VS Code):
//
//	go run main.go                 -> http://localhost:8080
//	go run main.go -addr :3000     -> outra porta (aceita ligações da rede local)
//
// Leitura de faturas por IA (recomendado: lê PDFs digitalizados e fotos):
//  1. Abra a página, bloco "Fatura do cliente" > "Chave da API", cole a chave e clique em Guardar.
//     A chave fica guardada de forma permanente neste computador (pasta de configuração do
//     utilizador, ficheiro simulador-energia-pt/config.json) e é carregada em cada arranque.
//  2. Alternativa: variável de ambiente ANTHROPIC_API_KEY (tem prioridade sobre a chave guardada).
//     Opcional: ANTHROPIC_MODEL para escolher outro modelo.
//
// Leitura LOCAL, sem API e sem custos (a fatura nunca sai do computador). Instale:
//
//	Tesseract (OCR de fotos/PDFs digitalizados):  winget install --id UB-Mannheim.TesseractOCR -e
//	     (no instalador, marque o idioma Português; ou copie por.traineddata para a pasta tessdata)
//	Poppler (leitura de PDFs):                    winget install --id oschwartz10612.Poppler -e
//	     (alternativas: choco install poppler tesseract  /  scoop install poppler tesseract)
//	Se os programas não ficarem no PATH, defina TESSERACT_PATH e POPPLER_PATH (pasta dos .exe).
//	Windows de 32 bits: o Poppler atual só existe em 64 bits. Use o Tesseract w32 e as
//	Xpdf command line tools (xpdfreader.com, pasta bin32: pdftotext.exe e pdftopng.exe);
//	o programa reconhece-as e POPPLER_PATH pode apontar para a pasta bin32.
//	Sem chave de API, o programa usa automaticamente esta leitura local.
//

package main

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================ SERVIDOR ============================

type recurso struct {
	tipo  string
	dados []byte
}

var recursos map[string]recurso

func montar() {
	bt := "`"
	txt := func(s string) []byte { return []byte(strings.ReplaceAll(s, "§", bt)) }
	b64 := func(s string) []byte {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			log.Fatal(err)
		}
		return b
	}
	recursos = map[string]recurso{
		"/index.html":           {"text/html; charset=utf-8", txt(indexHTML)},
		"/style.css":            {"text/css; charset=utf-8", txt(styleCSS)},
		"/app.js":               {"text/javascript; charset=utf-8", txt(appJS)},
		"/engine.js":            {"text/javascript; charset=utf-8", txt(engineJS)},
		"/sw.js":                {"text/javascript; charset=utf-8", txt(swJS)},
		"/manifest.webmanifest": {"application/manifest+json", txt(manifestJSON)},
		"/icon.svg":             {"image/svg+xml", txt(iconSVG)},
		"/icon-192.png":         {"image/png", b64(icon192)},
		"/icon-512.png":         {"image/png", b64(icon512)},
	}
}

func estatico(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	rec, ok := recursos[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", rec.tipo)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if p == "/sw.js" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Write(rec.dados)
}

func erroJSON(w http.ResponseWriter, codigo int, msg string) {
	w.WriteHeader(codigo)
	json.NewEncoder(w).Encode(map[string]string{"erro": msg})
}

func apiPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "ok")
}

func apiEstado(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(estadoChave())
}

func apiCalcular(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		erroJSON(w, http.StatusMethodNotAllowed, "usar POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var q Request
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		erroJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	json.NewEncoder(w).Encode(Calcular(q))
}

func exportar(dir string) error {
	for nome, rec := range recursos {
		destino := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(nome, "/")))
		if err := os.MkdirAll(filepath.Dir(destino), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destino, rec.dados, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	addr := flag.String("addr", "localhost:8080", "endereço do servidor")
	export := flag.String("export", "", "exporta o site estático para esta pasta e termina (ex.: public)")
	flag.Parse()
	montar()
	carregarConfig()

	if *export != "" {
		if err := exportar(*export); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Site exportado para:", *export)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", apiPing)
	mux.HandleFunc("/api/estado", apiEstado)
	mux.HandleFunc("/api/chave", apiChave)
	mux.HandleFunc("/api/ferramentas", apiFerramentas)
	mux.HandleFunc("/api/calcular", apiCalcular)
	mux.HandleFunc("/api/extrair", apiExtrair)
	mux.HandleFunc("/api/ler-texto", apiLerTexto)
	mux.HandleFunc("/", estatico)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      150 * time.Second, // a leitura por IA pode demorar
	}
	url := *addr
	if strings.HasPrefix(url, ":") {
		url = "localhost" + url
	}
	fmt.Println("Simulador a correr em http://" + url)
	k, origem := obterChave()
	switch {
	case k == "":
		fmt.Println("Leitura de faturas: modo básico. Guarde a chave da IA na página (Fatura do cliente > Chave da API).")
	case origem == "ambiente":
		fmt.Println("Leitura de faturas por IA ativa (chave da variável de ambiente, modelo " + modeloIA() + ").")
	default:
		fmt.Println("Leitura de faturas por IA ativa (chave guardada neste computador, modelo " + modeloIA() + ").")
	}
	log.Fatal(srv.ListenAndServe())
}

// ======================= CHAVE DA API (permanente) =======================

var (
	chaveMu        sync.RWMutex
	chaveGuardada  string
	pastaPoppler   string
	pastaTesseract string
)

type configFicheiro struct {
	AnthropicKey  string `json:"anthropicKey"`
	PopplerPath   string `json:"popplerPath"`
	TesseractPath string `json:"tesseractPath"`
}

func caminhoConfig() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "simulador-energia-pt", "config.json"), nil
}

func lerConfig() configFicheiro {
	var c configFicheiro
	p, err := caminhoConfig()
	if err != nil {
		return c
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	json.Unmarshal(b, &c)
	return c
}

func gravarConfig(c configFicheiro) error {
	p, err := caminhoConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(c)
	return os.WriteFile(p, b, 0o600)
}

func carregarConfig() {
	c := lerConfig()
	chaveMu.Lock()
	chaveGuardada = strings.TrimSpace(c.AnthropicKey)
	pastaPoppler = strings.TrimSpace(c.PopplerPath)
	pastaTesseract = strings.TrimSpace(c.TesseractPath)
	chaveMu.Unlock()
}

func pastasGuardadas() (poppler, tesseract string) {
	chaveMu.RLock()
	defer chaveMu.RUnlock()
	return pastaPoppler, pastaTesseract
}

func guardarFerramentas(poppler, tesseract string) error {
	c := lerConfig()
	c.PopplerPath, c.TesseractPath = poppler, tesseract
	if err := gravarConfig(c); err != nil {
		return err
	}
	chaveMu.Lock()
	pastaPoppler, pastaTesseract = poppler, tesseract
	chaveMu.Unlock()
	return nil
}

// obterChave devolve a chave ativa e a origem ("ambiente" ou "ficheiro").
func obterChave() (chave, origem string) {
	if k := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); k != "" {
		return k, "ambiente"
	}
	chaveMu.RLock()
	defer chaveMu.RUnlock()
	if chaveGuardada != "" {
		return chaveGuardada, "ficheiro"
	}
	return "", ""
}

func guardarChave(k string) error {
	c := lerConfig()
	c.AnthropicKey = k
	if err := gravarConfig(c); err != nil {
		return err
	}
	chaveMu.Lock()
	chaveGuardada = k
	chaveMu.Unlock()
	return nil
}

func removerChave() error {
	c := lerConfig()
	c.AnthropicKey = ""
	if err := gravarConfig(c); err != nil {
		return err
	}
	chaveMu.Lock()
	chaveGuardada = ""
	chaveMu.Unlock()
	return nil
}

// estadoChave nunca devolve a chave: apenas os últimos 4 caracteres.
func estadoChave() map[string]any {
	k, origem := obterChave()
	fim := ""
	if len(k) >= 4 {
		fim = k[len(k)-4:]
	}
	return map[string]any{
		"ia": k != "", "modelo": modeloIA(), "origem": origem, "fim": fim,
		"tesseract": ferramenta("tesseract") != "",
		"poppler":   ferramenta("pdftotext") != "",
		"paginas":   ferramenta("pdftoppm") != "" || ferramenta("pdftopng") != "",
	}
}

// pedidoLocal só aceita pedidos vindos do próprio computador, a partir de localhost.
func pedidoLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || o == "http://"+r.Host || o == "https://"+r.Host
}

func apiChave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !pedidoLocal(r) {
		erroJSON(w, http.StatusForbidden, "a chave só pode ser alterada a partir do próprio computador (localhost)")
		return
	}
	switch r.Method {
	case http.MethodPost:
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			erroJSON(w, http.StatusUnsupportedMediaType, "usar JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
		var corpo struct {
			Chave string `json:"chave"`
		}
		if err := json.NewDecoder(r.Body).Decode(&corpo); err != nil {
			erroJSON(w, http.StatusBadRequest, "pedido inválido")
			return
		}
		k := strings.TrimSpace(corpo.Chave)
		if len(k) < 20 || strings.ContainsAny(k, " \t\r\n\"") {
			erroJSON(w, http.StatusBadRequest, "chave com formato inválido")
			return
		}
		if err := guardarChave(k); err != nil {
			erroJSON(w, http.StatusInternalServerError, "não foi possível guardar a chave: "+err.Error())
			return
		}
	case http.MethodDelete:
		if err := removerChave(); err != nil {
			erroJSON(w, http.StatusInternalServerError, "não foi possível remover a chave: "+err.Error())
			return
		}
	default:
		erroJSON(w, http.StatusMethodNotAllowed, "usar POST ou DELETE")
		return
	}
	json.NewEncoder(w).Encode(estadoChave())
}

func caminhosFerramentas() map[string]string {
	return map[string]string{
		"pdftotext": ferramenta("pdftotext"),
		"pdftoppm":  ferramenta("pdftoppm"),
		"pdftopng":  ferramenta("pdftopng"),
		"tesseract": ferramenta("tesseract"),
	}
}

func apiFerramentas(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !pedidoLocal(r) {
		erroJSON(w, http.StatusForbidden, "só é possível alterar a partir do próprio computador (localhost)")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			erroJSON(w, http.StatusUnsupportedMediaType, "usar JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
		var corpo struct {
			Poppler   string `json:"poppler"`
			Tesseract string `json:"tesseract"`
		}
		if err := json.NewDecoder(r.Body).Decode(&corpo); err != nil {
			erroJSON(w, http.StatusBadRequest, "pedido inválido")
			return
		}
		limpar := func(p string) string { return strings.Trim(strings.TrimSpace(p), "\"") }
		pp, tt := limpar(corpo.Poppler), limpar(corpo.Tesseract)
		for _, p := range []string{pp, tt} {
			if p == "" {
				continue
			}
			if _, err := os.Stat(p); err != nil {
				erroJSON(w, http.StatusBadRequest, "pasta ou ficheiro não encontrado: "+p)
				return
			}
		}
		if err := guardarFerramentas(pp, tt); err != nil {
			erroJSON(w, http.StatusInternalServerError, "não foi possível guardar: "+err.Error())
			return
		}
	default:
		erroJSON(w, http.StatusMethodNotAllowed, "usar GET ou POST")
		return
	}
	pp, tt := pastasGuardadas()
	json.NewEncoder(w).Encode(map[string]any{"encontradas": caminhosFerramentas(), "poppler": pp, "tesseract": tt})
}

// ======================= LEITURA DE FATURAS =======================

// Extracao é o resultado da leitura de uma fatura. Campos a null = não encontrado.
type Extracao struct {
	Comercializador string                `json:"comercializador"`
	Cliente         string                `json:"cliente"`
	Observacoes     string                `json:"observacoes"`
	Periodo         string                `json:"periodo"`
	Eletricidade    *ExtracaoEletricidade `json:"eletricidade"`
	Gas             *ExtracaoGas          `json:"gas"`
	Metodo          string                `json:"metodo"`    // "ia" | "ocr" | "texto"
	TextoLido       string                `json:"textoLido"` // texto extraído (diagnóstico, só leitura local)
}

type ExtracaoEletricidade struct {
	PotenciaKva    *float64 `json:"potenciaKva"`
	ConsumoKwh     *float64 `json:"consumoKwh"`
	Dias           *int     `json:"dias"`
	Tarifa         string   `json:"tarifa"`
	VazioPct       *float64 `json:"vazioPct"`
	PotDia         *float64 `json:"potDia"`
	PrecoSimples   *float64 `json:"precoSimples"`
	PrecoVazio     *float64 `json:"precoVazio"`
	PrecoForaVazio *float64 `json:"precoForaVazio"`
	DescontoPct    *float64 `json:"descontoPct"`
	OutrosMes      *float64 `json:"outrosMes"`
	TotalFatura    *float64 `json:"totalFatura"`
}

type ExtracaoGas struct {
	Escalao      *float64 `json:"escalao"`
	ConsumoKwh   *float64 `json:"consumoKwh"`
	Dias         *int     `json:"dias"`
	TermoFixoDia *float64 `json:"termoFixoDia"`
	PrecoKwh     *float64 `json:"precoKwh"`
	DescontoPct  *float64 `json:"descontoPct"`
	OutrosMes    *float64 `json:"outrosMes"`
	TotalFatura  *float64 `json:"totalFatura"`
}

const promptIA = `Lê esta fatura de energia de Portugal (eletricidade e/ou gás natural) e devolve APENAS um objeto JSON válido, sem texto antes ou depois e sem formatação markdown, com este formato (usa null quando o dado não existir na fatura; números com ponto decimal e sem unidades):
{
  "comercializador": "nome comercial do fornecedor (ex.: EDP Comercial, Endesa, Galp, Iberdrola, Goldenergy, SU Eletricidade)",
  "cliente": "nome do titular do contrato",
  "periodo": "período de faturação (datas), ex.: 23 jul a 10 ago 2026",
  "observacoes": "avisos curtos sobre dados incertos ou deduzidos",
  "eletricidade": null ou {
    "potenciaKva": potência contratada em kVA,
    "consumoKwh": consumo total faturado no período em kWh,
    "dias": número de dias do período de faturação,
    "tarifa": "simples" ou "bi" (usa "bi" também para tri-horário),
    "vazioPct": percentagem do consumo em vazio (0 a 100) se houver consumos por período, senão null,
    "potDia": preço da potência contratada em €/dia sem IVA,
    "precoSimples": preço da energia em €/kWh sem IVA (tarifa simples),
    "precoVazio": preço em €/kWh sem IVA no vazio (bi-horário),
    "precoForaVazio": preço em €/kWh sem IVA fora do vazio (se houver cheia e ponta, usa a média ponderada pelo consumo),
    "descontoPct": desconto percentual na energia, se existir, senão 0,
    "outrosMes": outros serviços cobrados em € sem IVA, excluindo IEC, taxa DGEG e contribuição audiovisual, senão 0,
    "totalFatura": total a pagar com IVA
  },
  "gas": null ou {
    "consumoKwh": consumo faturado em kWh,
    "dias": número de dias do período,
    "escalao": escalão de consumo do gás (1 a 4), senão null,
    "termoFixoDia": termo fixo em €/dia sem IVA,
    "precoKwh": preço da energia em €/kWh sem IVA,
    "descontoPct": desconto percentual, senão 0,
    "outrosMes": outros serviços em € sem IVA, senão 0,
    "totalFatura": total a pagar com IVA
  }
}
Regras: se a fatura separar o preço do comercializador das tarifas de acesso às redes, soma-os no preço unitário (€/dia ou €/kWh) e diz isso nas observações. Se os preços surgirem com IVA incluído, converte para sem IVA e diz isso nas observações. Não incluas NIF, morada, IBAN nem outros dados pessoais além do nome do titular.`

func modeloIA() string {
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return m
	}
	return "claude-sonnet-5-5"
}

func apiExtrair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		erroJSON(w, http.StatusMethodNotAllowed, "usar POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		erroJSON(w, http.StatusBadRequest, "ficheiro inválido ou demasiado grande (máximo 20 MB)")
		return
	}
	f, cab, err := r.FormFile("ficheiro")
	if err != nil {
		erroJSON(w, http.StatusBadRequest, "nenhum ficheiro recebido")
		return
	}
	defer f.Close()
	dados, err := io.ReadAll(io.LimitReader(f, 20<<20))
	if err != nil || len(dados) == 0 {
		erroJSON(w, http.StatusBadRequest, "não foi possível ler o ficheiro")
		return
	}
	ex, err := extrairFatura(cab.Filename, dados)
	if err != nil {
		erroJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	json.NewEncoder(w).Encode(ex)
}

// apiLerTexto devolve apenas o texto lido de um ficheiro (PDF, imagem por OCR, Word, Excel...).
// A interpretação da tabela de preços é feita no navegador, para funcionar também offline.
func apiLerTexto(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		erroJSON(w, http.StatusMethodNotAllowed, "usar POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		erroJSON(w, http.StatusBadRequest, "ficheiro inválido ou demasiado grande (máximo 20 MB)")
		return
	}
	f, cab, err := r.FormFile("ficheiro")
	if err != nil {
		erroJSON(w, http.StatusBadRequest, "nenhum ficheiro recebido")
		return
	}
	defer f.Close()
	dados, err := io.ReadAll(io.LimitReader(f, 20<<20))
	if err != nil || len(dados) == 0 {
		erroJSON(w, http.StatusBadRequest, "não foi possível ler o ficheiro")
		return
	}
	tipo := detetarTipo(cab.Filename, dados)
	texto, metodo, notas := textoLocal(tipo, dados)
	if strings.TrimSpace(texto) == "" {
		msg := "Não consegui ler texto neste ficheiro."
		if len(notas) > 0 {
			msg += " " + strings.Join(notas, " ")
		}
		erroJSON(w, http.StatusUnprocessableEntity, msg)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"texto": limitarTexto(texto, 60000), "metodo": metodo, "notas": notas})
}

func extrairFatura(nome string, dados []byte) (*Extracao, error) {
	tipo := detetarTipo(nome, dados)
	aviso := ""

	if chave, _ := obterChave(); chave != "" {
		blocos, err := blocosParaIA(tipo, dados)
		if err == nil {
			var txt string
			txt, err = chamarClaude(chave, blocos)
			if err == nil {
				var ex *Extracao
				ex, err = lerJSONExtracao(txt)
				if err == nil {
					ex.Metodo = "ia"
					return ex, nil
				}
			}
		}
		switch {
		case strings.Contains(err.Error(), "401"):
			aviso = "A chave da API foi recusada (inválida): remova-a ou guarde uma chave válida. Usada a leitura local. "
		case strings.Contains(err.Error(), "credit balance"):
			aviso = "A conta da API está sem saldo. Usada a leitura local. "
		default:
			aviso = "Leitura por IA indisponível (" + err.Error() + "). Usada a leitura local. "
		}
	}

	texto, metodo, notas := textoLocal(tipo, dados)
	notaStr := strings.Join(notas, " ")
	if strings.TrimSpace(texto) == "" {
		msg := "Não consegui ler texto neste ficheiro."
		if notaStr != "" {
			msg += " " + notaStr
		}
		return nil, errors.New(aviso + msg)
	}
	ex := extrairHeuristico(texto)
	ex.TextoLido = limitarTexto(texto, 8000)
	if ex.Eletricidade == nil && ex.Gas == nil {
		ex.Metodo = metodo
		ex.Observacoes = strings.TrimSpace(aviso + notaStr + " " + ex.Observacoes)
		return ex, nil
	}
	ex.Metodo = metodo
	ex.Observacoes = strings.TrimSpace(aviso + notaStr + " " + ex.Observacoes + " Leitura local: confirme todos os valores.")
	return ex, nil
}

func limitarTexto(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// ---------- Leitura local: Poppler + Tesseract ----------

// ferramenta devolve o caminho de um executável (PATH, variáveis TESSERACT_PATH/POPPLER_PATH
// ou pastas de instalação comuns no Windows) ou "" se não existir.
// pastasLocais devolve as pastas junto ao projeto onde as ferramentas podem estar
// (por exemplo ferramentas\pdftotext.exe ao lado do main.go), para funcionar sem instalação.
func pastasLocais() []string {
	var bases []string
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	if ex, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(ex))
	}
	var out []string
	for _, b := range bases {
		for _, sub := range []string{"", "ferramentas", "ferramentas/xpdf", "ferramentas/tesseract", "ferramentas/Tesseract-OCR", "bin", "xpdf", "tesseract", "Tesseract-OCR"} {
			out = append(out, filepath.Join(b, filepath.FromSlash(sub)))
		}
		for _, padrao := range []string{"xpdf-tools-win*/bin32", "ferramentas/xpdf-tools-win*/bin32", "ferramentas/*/bin32"} {
			if m, _ := filepath.Glob(filepath.Join(b, filepath.FromSlash(padrao))); len(m) > 0 {
				out = append(out, m...)
			}
		}
	}
	return out
}

func procurarEm(dirs []string, exe string) string {
	for _, d := range dirs {
		d = strings.Trim(strings.TrimSpace(d), "\"")
		if d == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(d), ".exe") {
			d = filepath.Dir(d)
		}
		cand := filepath.Join(d, exe)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// ferramenta devolve o caminho de um executável ou "" se não existir. Ordem: pasta indicada
// na página, variáveis TESSERACT_PATH/POPPLER_PATH, PATH e pastas de instalação comuns
// (inclui o pdftotext que vem com o Git para Windows).
func ferramenta(nome string) string {
	exe := nome
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	pp, tt := pastasGuardadas()
	var indicadas []string
	if nome == "tesseract" {
		indicadas = []string{tt, os.Getenv("TESSERACT_PATH")}
	} else {
		indicadas = []string{pp, os.Getenv("POPPLER_PATH")}
	}
	if p := procurarEm(indicadas, exe); p != "" {
		return p
	}
	if p := procurarEm(pastasLocais(), exe); p != "" {
		return p
	}
	if p, err := exec.LookPath(nome); err == nil {
		return p
	}
	local := os.Getenv("LOCALAPPDATA")
	var comuns []string
	if nome == "tesseract" {
		comuns = []string{
			`C:\Program Files\Tesseract-OCR`, `C:\Program Files (x86)\Tesseract-OCR`,
			filepath.Join(local, `Programs\Tesseract-OCR`),
		}
	} else {
		comuns = []string{
			`C:\Program Files\Git\mingw32\bin`, `C:\Program Files (x86)\Git\mingw32\bin`,
			`C:\Program Files\Git\mingw64\bin`, `C:\Program Files\Git\usr\bin`,
			filepath.Join(local, `Programs\Git\mingw32\bin`), filepath.Join(local, `Programs\Git\mingw64\bin`),
			`C:\poppler\Library\bin`, `C:\poppler\bin`,
			`C:\Program Files\poppler\Library\bin`, `C:\Program Files\poppler\bin`,
		}
		for _, padrao := range []string{
			`C:\Program Files\poppler*\Library\bin`, `C:\Program Files\poppler*\bin`,
			`C:\poppler*\Library\bin`, `C:\poppler*\bin`,
			`C:\xpdf-tools-win*\bin32`, `C:\xpdf*\bin32`, `C:\xpdf-tools-win*\bin64`, `C:\xpdf*\bin64`,
			`C:\Program Files\xpdf*\bin32`, `C:\Program Files (x86)\xpdf*\bin32`,
			filepath.Join(local, `Microsoft\WinGet\Packages\oschwartz10612.Poppler*\poppler*\Library\bin`),
			filepath.Join(os.Getenv("USERPROFILE"), `scoop\apps\poppler\current\Library\bin`),
		} {
			if m, _ := filepath.Glob(padrao); len(m) > 0 {
				comuns = append(comuns, m...)
			}
		}
	}
	return procurarEm(comuns, exe)
}

func executar(limite time.Duration, exe string, args ...string) ([]byte, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), limite)
	defer cancelar()
	return exec.CommandContext(ctx, exe, args...).Output()
}

func ocrFicheiro(tess, caminho string) (string, error) {
	var extra []string
	if td := filepath.Join(filepath.Dir(tess), "tessdata"); dirExiste(td) {
		extra = []string{"--tessdata-dir", td}
	}
	corre := func(lang string) ([]byte, error) {
		args := append([]string{caminho, "stdout"}, extra...)
		return executar(90*time.Second, tess, append(args, "-l", lang, "--psm", "6")...)
	}
	out, err := corre("por+eng")
	if err != nil { // idioma português em falta: tenta só inglês (os números lêem-se bem)
		out, err = corre("eng")
	}
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func dirExiste(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func ocrImagem(tess string, dados []byte, ext string) (string, error) {
	dir, err := os.MkdirTemp("", "fatura-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	img := filepath.Join(dir, "img"+ext)
	if err := os.WriteFile(img, dados, 0o600); err != nil {
		return "", err
	}
	return ocrFicheiro(tess, img)
}

func pdfParaTexto(pdftotext string, dados []byte) string {
	dir, err := os.MkdirTemp("", "fatura-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, dados, 0o600); err != nil {
		return ""
	}
	out, err := executar(60*time.Second, pdftotext, "-layout", "-enc", "UTF-8", "-l", "6", in, "-")
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out)
}

func ocrPDF(pdftoppm, tess string, dados []byte) (string, error) {
	dir, err := os.MkdirTemp("", "fatura-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, dados, 0o600); err != nil {
		return "", err
	}
	base := []string{"-r", "200", "-f", "1", "-l", "4"}
	saida := filepath.Join(dir, "pag")
	juntar := func(extra ...string) []string {
		a := append([]string{}, base...)
		a = append(a, extra...)
		return append(a, in, saida)
	}
	tentativas := [][]string{juntar()} // pdftopng (Xpdf) já gera PNG
	if !strings.Contains(strings.ToLower(filepath.Base(pdftoppm)), "pdftopng") {
		// pdftoppm: o Poppler aceita -png; o do Xpdf não e gera PPM (o Tesseract também o lê)
		tentativas = [][]string{juntar("-png"), juntar()}
	}
	var errFinal error
	for _, a := range tentativas {
		if _, err := executar(120*time.Second, pdftoppm, a...); err != nil {
			errFinal = err
			continue
		}
		errFinal = nil
		break
	}
	if errFinal != nil {
		return "", errFinal
	}
	var paginas []string
	for _, padrao := range []string{"pag*.png", "pag*.ppm"} {
		m, _ := filepath.Glob(filepath.Join(dir, padrao))
		paginas = append(paginas, m...)
	}
	if len(paginas) == 0 {
		return "", errors.New("PDF sem páginas legíveis")
	}
	var sb strings.Builder
	for _, p := range paginas {
		if t, err := ocrFicheiro(tess, p); err == nil {
			sb.WriteString(t)
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

func extensaoImagem(tipo string) string {
	switch tipo {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	case "image/bmp":
		return ".bmp"
	}
	return ".png"
}

// textoLocal devolve o texto, o método usado ("texto" ou "ocr") e notas para o utilizador.
func textoLocal(tipo string, dados []byte) (string, string, []string) {
	var notas []string
	pdftotext, pdftoppm, tess := ferramenta("pdftotext"), ferramenta("pdftopng"), ferramenta("tesseract")
	if pdftoppm == "" {
		pdftoppm = ferramenta("pdftoppm")
	}

	switch tipo {
	case "pdf":
		var texto string
		if pdftotext != "" {
			texto = pdfParaTexto(pdftotext, dados)
		} else {
			texto = textoPDF(dados)
			notas = append(notas, "Não encontrei o pdftotext: usada a leitura interna de PDF (pouco fiável). Em «Ferramentas de leitura local» indique a pasta do pdftotext (por exemplo C:\\Program Files\\Git\\mingw32\\bin).")
		}
		if len(strings.Fields(texto)) >= 15 {
			return texto, "texto", notas
		}
		// pouco texto: provavelmente um PDF digitalizado -> OCR
		if pdftoppm == "" || tess == "" {
			notas = append(notas, "Parece um PDF digitalizado: para o ler são precisos o Tesseract e o pdftoppm/pdftopng (indique as pastas em «Ferramentas de leitura local»).")
			return texto, "texto", notas
		}
		ocr, err := ocrPDF(pdftoppm, tess, dados)
		if err != nil {
			notas = append(notas, "OCR falhou: "+err.Error())
			return texto, "texto", notas
		}
		return ocr, "ocr", notas
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/tiff", "image/bmp":
		if tess == "" {
			notas = append(notas, "Para ler fotos é preciso o Tesseract (OCR): instale-o e indique a pasta em «Ferramentas de leitura local».")
			return "", "texto", notas
		}
		t, err := ocrImagem(tess, dados, extensaoImagem(tipo))
		if err != nil {
			notas = append(notas, "OCR falhou: "+err.Error())
			return "", "ocr", notas
		}
		return t, "ocr", notas
	case "heic":
		notas = append(notas, "Fotos HEIC (iPhone) não são suportadas: tire uma captura de ecrã ou converta para JPG/PNG.")
		return "", "texto", notas
	case "ole":
		notas = append(notas, "Formato antigo do Office (.doc/.xls): guarde como .docx/.xlsx ou PDF e envie de novo.")
		return "", "texto", notas
	case "zip", "desconhecido":
		notas = append(notas, "Formato não reconhecido. Use PDF, imagem (JPG, PNG, WEBP, TIFF, BMP), Word (.docx), Excel (.xlsx), ODT/ODS, RTF ou texto.")
		return "", "texto", notas
	}
	return textoDoFicheiro(tipo, dados), "texto", notas
}

func detetarTipo(nome string, d []byte) string {
	switch {
	case bytes.HasPrefix(d, []byte("%PDF")):
		return "pdf"
	case bytes.HasPrefix(d, []byte("\x89PNG")):
		return "image/png"
	case bytes.HasPrefix(d, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(d, []byte("GIF8")):
		return "image/gif"
	case len(d) > 12 && bytes.Equal(d[0:4], []byte("RIFF")) && bytes.Equal(d[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(d, []byte("II*\x00")) || bytes.HasPrefix(d, []byte("MM\x00*")):
		return "image/tiff"
	case bytes.HasPrefix(d, []byte("{\\rtf")):
		return "rtf"
	case bytes.HasPrefix(d, []byte{0xD0, 0xCF, 0x11, 0xE0}):
		return "ole"
	}
	ext := strings.ToLower(filepath.Ext(nome))
	if bytes.HasPrefix(d, []byte("PK\x03\x04")) {
		switch ext {
		case ".docx":
			return "docx"
		case ".xlsx":
			return "xlsx"
		case ".odt":
			return "odt"
		case ".ods":
			return "ods"
		}
		return "zip"
	}
	switch ext {
	case ".txt", ".csv", ".html", ".htm", ".xml", ".json", ".md":
		return "texto"
	case ".bmp":
		if bytes.HasPrefix(d, []byte("BM")) {
			return "image/bmp"
		}
	case ".heic", ".heif":
		return "heic"
	}
	return "desconhecido"
}

func blocosParaIA(tipo string, dados []byte) ([]map[string]any, error) {
	var bloco map[string]any
	switch tipo {
	case "pdf":
		bloco = map[string]any{"type": "document", "source": map[string]any{
			"type": "base64", "media_type": "application/pdf",
			"data": base64.StdEncoding.EncodeToString(dados)}}
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		bloco = map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": tipo,
			"data": base64.StdEncoding.EncodeToString(dados)}}
	case "docx", "xlsx", "odt", "ods", "rtf", "texto":
		t := textoDoFicheiro(tipo, dados)
		if strings.TrimSpace(t) == "" {
			return nil, errors.New("ficheiro sem texto legível")
		}
		if len(t) > 60000 {
			t = t[:60000]
		}
		bloco = map[string]any{"type": "text", "text": "Conteúdo da fatura:\n\n" + t}
	default:
		return nil, errors.New("formato não suportado (use PDF, imagem JPG/PNG/WEBP, Word, Excel ou texto)")
	}
	return []map[string]any{bloco, {"type": "text", "text": promptIA}}, nil
}

func chamarClaude(chave string, blocos []map[string]any) (string, error) {
	corpo, err := json.Marshal(map[string]any{
		"model":      modeloIA(),
		"max_tokens": 1500,
		"messages":   []map[string]any{{"role": "user", "content": blocos}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(corpo))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", chave)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	cli := &http.Client{Timeout: 120 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return "", errors.New("sem ligação à API")
	}
	defer resp.Body.Close()
	resposta, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(resposta))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("API devolveu %d: %s", resp.StatusCode, msg)
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resposta, &r); err != nil {
		return "", errors.New("resposta da API ilegível")
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

func lerJSONExtracao(txt string) (*Extracao, error) {
	i, j := strings.Index(txt, "{"), strings.LastIndex(txt, "}")
	if i < 0 || j <= i {
		return nil, errors.New("resposta da IA sem JSON")
	}
	var ex Extracao
	if err := json.Unmarshal([]byte(txt[i:j+1]), &ex); err != nil {
		return nil, fmt.Errorf("resposta da IA inválida: %w", err)
	}
	return &ex, nil
}

// ---------- Leitura básica de texto (sem IA) ----------

var (
	reTags         = regexp.MustCompile(`<[^>]*>`)
	rePDFStream    = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	rePDFTok       = regexp.MustCompile(`\(((?:\\.|[^\\)])*)\)|Tj|TJ|ET|Td|TD|T\*`)
	reKva          = regexp.MustCompile(`(?i)(\d{1,2}(?:[.,]\d{1,2})?)\s*kva`)
	reDias         = regexp.MustCompile(`(?i)(\d{1,3})\s*dias`)
	rePrecoDia     = regexp.MustCompile(`(?i)(\d+[.,]\d{3,6})\s*(?:€|eur)?\s*/\s*dia`)
	rePrecoKwh     = regexp.MustCompile(`(?i)(\d+[.,]\d{3,6})\s*(?:€|eur)?\s*/\s*kwh`)
	reKwh          = regexp.MustCompile(`(?i)(\d{1,5}(?:[.,]\d{1,3})?)\s*kwh`)
	reTotalPagar   = regexp.MustCompile(`(?i)(?:total\s+a\s+pagar|valor\s+a\s+pagar|total\s+da\s+fatura)[^0-9]{0,40}(\d{1,5}[.,]\d{2})`)
	reEscalao      = regexp.MustCompile(`(?i)escal[ãa]o\s*(?:de\s*consumo\s*)?(\d)`)
	rePeriodo      = regexp.MustCompile(`(?i)per[ií]odo\s+de\s+fatura[çc][ãa]o:?\s*([^\n]{5,60})`)
	rePotDiasPreco = regexp.MustCompile(`(?i)(\d{1,3})\s*dias\s+(\d+[.,]\d{3,6})`)
	reKwhPreco     = regexp.MustCompile(`(?i)(\d{1,5}(?:[.,]\d{1,3})?)\s*kwh\s+(\d+[.,]\d{3,6})`)
	reDescAntes    = regexp.MustCompile(`(?i)(\d{1,2}(?:[.,]\d{1,2})?)\s*%\s*desconto`)
	reValorFatura  = regexp.MustCompile(`(?i)\bvalor:\s*(\d{1,5}[.,]\d{2})`)
	reDesconto     = regexp.MustCompile(`(?i)desconto[^0-9\n]{0,30}(\d{1,2}(?:[.,]\d{1,2})?)\s*%`)
)

var comercializadores = []string{
	"EDP Comercial", "EDP", "Endesa", "Iberdrola", "Galp", "Goldenergy", "Gold Energy",
	"SU Eletricidade", "Repsol", "Luzboa", "Plenitude", "Ibelectra", "MEO Energia",
	"Coopérnico", "Luzigás", "Elergone", "Axpo", "Nossa Energia", "Ylce", "Audax", "Fortia",
}

func pf(x float64) *float64 { return &x }

func pi(x int) *int { return &x }

func paraNum(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	return v, err == nil
}

func removerTags(s string) string {
	for _, fimLinha := range []string{"</w:p>", "</w:tr>", "</row>", "</tr>", "</table:table-row>", "</text:p>"} {
		s = strings.ReplaceAll(s, fimLinha, "\n")
	}
	for _, fimCelula := range []string{"</w:tc>", "</td>", "</th>", "</table:table-cell>"} {
		s = strings.ReplaceAll(s, fimCelula, "  ")
	}
	s = reTags.ReplaceAllString(s, " ")
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'").Replace(s)
}

func textoZip(dados []byte, nomes ...string) string {
	zr, err := zip.NewReader(bytes.NewReader(dados), int64(len(dados)))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, nome := range nomes {
		for _, f := range zr.File {
			if f.Name != nome {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(rc, 20<<20))
			rc.Close()
			sb.WriteString(removerTags(string(b)))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func desescaparPDF(s string) string {
	var out []rune
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, rune(c))
			continue
		}
		i++
		switch n := s[i]; {
		case n == 'n' || n == 'r' || n == 't' || n == 'b' || n == 'f':
			out = append(out, ' ')
		case n >= '0' && n <= '7':
			v := int(n - '0')
			for k := 0; k < 2 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7'; k++ {
				i++
				v = v*8 + int(s[i]-'0')
			}
			out = append(out, rune(v))
		default:
			out = append(out, rune(n))
		}
	}
	return string(out)
}

// textoPDF extrai texto de PDFs digitais simples. PDFs digitalizados ou com fontes
// codificadas não são suportados aqui (usar a leitura por IA).
func textoPDF(d []byte) string {
	var sb strings.Builder
	for _, m := range rePDFStream.FindAllSubmatch(d, -1) {
		fluxo := m[1]
		if zr, err := zlib.NewReader(bytes.NewReader(fluxo)); err == nil {
			if u, _ := io.ReadAll(io.LimitReader(zr, 50<<20)); len(u) > 0 {
				fluxo = u
			}
			zr.Close()
		}
		s := string(fluxo)
		if !strings.Contains(s, "BT") {
			continue
		}
		for _, t := range rePDFTok.FindAllStringSubmatch(s, -1) {
			if strings.HasPrefix(t[0], "(") {
				sb.WriteString(desescaparPDF(t[1]))
			} else {
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

var (
	reXSI    = regexp.MustCompile(`(?s)<si>(.*?)</si>`)
	reXT     = regexp.MustCompile(`(?s)<t[^>]*>(.*?)</t>`)
	reXRow   = regexp.MustCompile(`(?s)<row[^>]*>(.*?)</row>`)
	reXCell  = regexp.MustCompile(`(?s)<c([^>]*?)(?:/>|>(.*?)</c>)`)
	reXV     = regexp.MustCompile(`(?s)<v>(.*?)</v>`)
	reXAttrT = regexp.MustCompile(`\bt="(\w+)"`)
)

func lerFicheiroZip(zr *zip.Reader, nome string) string {
	for _, f := range zr.File {
		if f.Name != nome {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 20<<20))
		rc.Close()
		return string(b)
	}
	return ""
}

func textoXLSX(dados []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(dados), int64(len(dados)))
	if err != nil {
		return ""
	}
	var partilhadas []string
	if ss := lerFicheiroZip(zr, "xl/sharedStrings.xml"); ss != "" {
		for _, m := range reXSI.FindAllStringSubmatch(ss, -1) {
			var sb strings.Builder
			for _, t := range reXT.FindAllStringSubmatch(m[1], -1) {
				sb.WriteString(t[1])
			}
			partilhadas = append(partilhadas, removerTags(sb.String()))
		}
	}
	var out strings.Builder
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "xl/worksheets/sheet") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		folha := lerFicheiroZip(zr, f.Name)
		for _, row := range reXRow.FindAllStringSubmatch(folha, -1) {
			var celulas []string
			for _, c := range reXCell.FindAllStringSubmatch(row[1], -1) {
				val := ""
				if v := reXV.FindStringSubmatch(c[2]); v != nil {
					val = v[1]
				} else if t := reXT.FindStringSubmatch(c[2]); t != nil {
					val = t[1]
				}
				if tm := reXAttrT.FindStringSubmatch(c[1]); tm != nil && tm[1] == "s" {
					if idx, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && idx >= 0 && idx < len(partilhadas) {
						val = partilhadas[idx]
					}
				}
				celulas = append(celulas, strings.TrimSpace(removerTags(val)))
			}
			out.WriteString(strings.Join(celulas, "  "))
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	return out.String()
}

var (
	reRTFPar = regexp.MustCompile(`\\par\b`)
	reRTFHex = regexp.MustCompile(`\\'([0-9a-fA-F]{2})`)
	reRTFCtl = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?|[{}]`)
)

func textoRTF(s string) string {
	s = reRTFPar.ReplaceAllString(s, "\n")
	s = reRTFHex.ReplaceAllStringFunc(s, func(m string) string {
		v, err := strconv.ParseUint(m[2:], 16, 8)
		if err != nil {
			return ""
		}
		return string(rune(v))
	})
	return reRTFCtl.ReplaceAllString(s, "")
}

// textoZipSe junta o texto dos ficheiros XML de um zip (docx, xlsx, odt, ods) escolhidos por quer.
func textoZipSe(dados []byte, quer func(string) bool) string {
	zr, err := zip.NewReader(bytes.NewReader(dados), int64(len(dados)))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, f := range zr.File {
		if !quer(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 20<<20))
		rc.Close()
		sb.WriteString(removerTags(string(b)))
		sb.WriteString("\n")
	}
	return sb.String()
}

func textoDoFicheiro(tipo string, dados []byte) string {
	switch tipo {
	case "pdf":
		return textoPDF(dados)
	case "docx":
		return textoZipSe(dados, func(n string) bool {
			return n == "word/document.xml" || strings.HasPrefix(n, "word/header") || strings.HasPrefix(n, "word/footer")
		})
	case "xlsx":
		return textoXLSX(dados)
	case "odt", "ods":
		return textoZipSe(dados, func(n string) bool { return n == "content.xml" })
	case "rtf":
		return textoRTF(string(dados))
	case "texto":
		t := string(dados)
		if strings.Contains(t, "<html") || strings.Contains(t, "<body") {
			t = removerTags(t)
		}
		return t
	}
	return ""
}

func detetarComercializador(t string) string {
	melhor, pos := "", -1
	for _, n := range comercializadores {
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(n) + `\b`)
		if err != nil {
			continue
		}
		loc := re.FindStringIndex(t)
		if loc == nil {
			continue
		}
		if pos < 0 || loc[0] < pos || (loc[0] == pos && len(n) > len(melhor)) {
			melhor, pos = n, loc[0]
		}
	}
	return melhor
}

// acumPreco guarda o 1.º preço do comercializador e o 1.º das tarifas de acesso às redes.
type acumPreco struct {
	com, ace       float64
	temCom, temAce bool
}

func (a *acumPreco) somar(v float64, acesso bool) {
	if acesso {
		if !a.temAce {
			a.ace, a.temAce = v, true
		}
	} else if !a.temCom {
		a.com, a.temCom = v, true
	}
}

func (a *acumPreco) valor() (float64, bool) {
	if !a.temCom && !a.temAce {
		return 0, false
	}
	return a.com + a.ace, true
}

func ehAcesso(l string) bool {
	return strings.Contains(l, "acesso") || strings.Contains(l, "redes") || strings.Contains(l, " tar ")
}

func extrairHeuristico(t string) *Extracao {
	t = strings.ReplaceAll(t, "\u00a0", " ")
	ex := &Extracao{Comercializador: detetarComercializador(t)}
	if m := rePeriodo.FindStringSubmatch(t); m != nil {
		ex.Periodo = strings.TrimSpace(m[1])
	}
	el := &ExtracaoEletricidade{Tarifa: "simples"}
	var notas []string
	var pot, simples, vazio, fora acumPreco
	var qVazio, qFora float64
	temPonta := false

	var qSimples, diasPot float64
	for _, ln := range strings.Split(t, "\n") {
		l := strings.ToLower(ln)
		acesso := ehAcesso(l)
		if strings.Contains(l, "potência") || strings.Contains(l, "potencia") || strings.Contains(l, "termo fixo") {
			if m := rePrecoDia.FindStringSubmatch(ln); m != nil {
				if v, ok := paraNum(m[1]); ok {
					pot.somar(v, acesso)
				}
			} else if m := rePotDiasPreco.FindStringSubmatch(ln); m != nil {
				// formato "19 dias  0,4347" (sem unidades)
				if d, ok := paraNum(m[1]); ok && d >= 1 && d <= 366 && !acesso {
					diasPot = d
				}
				if v, ok := paraNum(m[2]); ok {
					pot.somar(v, acesso)
				}
			}
		}

		var precoStr, qtdStr string
		if m := rePrecoKwh.FindStringSubmatch(ln); m != nil {
			precoStr = m[1]
			if q := reKwh.FindStringSubmatch(ln); q != nil {
				qtdStr = q[1]
			}
		} else if m := reKwhPreco.FindStringSubmatch(ln); m != nil {
			// formato "89 kWh  0,1671" (sem unidades)
			qtdStr, precoStr = m[1], m[2]
		} else {
			continue
		}
		v, ok := paraNum(precoStr)
		if !ok {
			continue
		}
		qtd := 0.0
		if x, ok2 := paraNum(qtdStr); ok2 {
			qtd = x
		}
		switch {
		case strings.Contains(l, "ponta"):
			temPonta = true
		case strings.Contains(l, "cheia") || (strings.Contains(l, "fora") && strings.Contains(l, "vazio")):
			fora.somar(v, acesso)
			if !acesso {
				qFora += qtd
			}
		case strings.Contains(l, "vazio"):
			vazio.somar(v, acesso)
			if !acesso {
				qVazio += qtd
			}
		case strings.Contains(l, "energia") || strings.Contains(l, "consumo") || strings.Contains(l, "gás") || strings.Contains(l, "gas natural"):
			simples.somar(v, acesso)
			if !acesso {
				qSimples += qtd
			}
		}
	}

	primeiro := func(re *regexp.Regexp) *float64 {
		if m := re.FindStringSubmatch(t); m != nil {
			if v, ok := paraNum(m[1]); ok {
				return pf(v)
			}
		}
		return nil
	}

	// Potência contratada (kVA)
	achouKva := false
	for _, m := range reKva.FindAllStringSubmatch(t, -1) {
		if v, ok := paraNum(m[1]); ok && v >= 1 && v <= 70 {
			el.PotenciaKva = pf(v)
			achouKva = true
			break
		}
	}
	if diasPot > 0 {
		el.Dias = pi(int(math.Round(diasPot)))
	} else if m := reDias.FindStringSubmatch(t); m != nil {
		if v, ok := paraNum(m[1]); ok && v >= 1 && v <= 366 {
			el.Dias = pi(int(math.Round(v)))
		}
	}

	// Preço da potência / termo fixo (€/dia)
	if v, ok := pot.valor(); ok {
		el.PotDia = pf(v)
		if pot.temCom && pot.temAce {
			notas = append(notas, "Preço da potência = comercializador + acesso às redes: confirme.")
		}
	} else {
		el.PotDia = primeiro(rePrecoDia)
	}

	// Preços da energia
	vv, okV := vazio.valor()
	vf, okF := fora.valor()
	if okV && okF {
		el.Tarifa = "bi"
		el.PrecoVazio = pf(vv)
		el.PrecoForaVazio = pf(vf)
		if qVazio > 0 && qFora > 0 {
			el.VazioPct = pf(math.Round(qVazio/(qVazio+qFora)*1000) / 10)
			el.ConsumoKwh = pf(qVazio + qFora)
		}
		if temPonta {
			notas = append(notas, "Há preço de ponta (tri-horário): usado o de fora de vazio/cheia; confirme.")
		}
	} else if vs, ok := simples.valor(); ok {
		el.PrecoSimples = pf(vs)
		if simples.temCom && simples.temAce {
			notas = append(notas, "Preço da energia = comercializador + acesso às redes: confirme.")
		}
	} else {
		el.PrecoSimples = primeiro(rePrecoKwh)
	}

	// Consumo: maior valor em kWh do documento, se não veio dos períodos
	if el.ConsumoKwh == nil && qSimples > 0 {
		el.ConsumoKwh = pf(qSimples)
	}
	if el.ConsumoKwh == nil {
		maior := 0.0
		for _, m := range reKwh.FindAllStringSubmatch(t, -1) {
			if v, ok := paraNum(m[1]); ok && v > maior && v < 20000 {
				maior = v
			}
		}
		if maior > 0 {
			el.ConsumoKwh = pf(maior)
		}
	}
	el.TotalFatura = primeiro(reTotalPagar)
	if el.TotalFatura == nil {
		el.TotalFatura = primeiro(reValorFatura)
	}
	for _, re := range []*regexp.Regexp{reDesconto, reDescAntes} {
		if m := re.FindStringSubmatch(t); m != nil {
			if v, ok := paraNum(m[1]); ok && v > 0 && v < 100 {
				el.DescontoPct = pf(v)
				break
			}
		}
	}

	baixo := strings.ToLower(t)
	temGas := strings.Contains(baixo, "gás natural") || strings.Contains(baixo, "gas natural")
	switch {
	case achouKva || el.PrecoVazio != nil || (el.PrecoSimples != nil && !temGas):
		ex.Eletricidade = el
		if temGas {
			notas = append(notas, "A fatura também tem gás natural: preencha o gás manualmente.")
		}
	case temGas:
		ex.Gas = &ExtracaoGas{
			Escalao:    primeiro(reEscalao),
			ConsumoKwh: el.ConsumoKwh, Dias: el.Dias, TermoFixoDia: el.PotDia,
			PrecoKwh: el.PrecoSimples, DescontoPct: el.DescontoPct, TotalFatura: el.TotalFatura,
		}
	}
	ex.Observacoes = strings.Join(notas, " ")
	return ex
}

// ======================= MOTOR DE CÁLCULO (Go) =======================

// Fiscal reúne os parâmetros legais (editáveis na interface).
type Fiscal struct {
	IECkWh                float64 `json:"iecKwh"`                // €/kWh
	DGEGMes               float64 `json:"dgegMes"`               // €/mês (eletricidade)
	CAVMes                float64 `json:"cavMes"`                // €/mês
	CAVAplica             bool    `json:"cavAplica"`             // CAV aplicável?
	IVANormal             float64 `json:"ivaNormal"`             // %
	IVAReduzido           float64 `json:"ivaReduzido"`           // %
	LimiteKwhReduzido     float64 `json:"limiteKwhReduzido"`     // kWh por 30 dias com IVA reduzido
	EnergiaReduzidaMaxKva float64 `json:"energiaReduzidaMaxKva"` // potência máx. para IVA reduzido na energia
	PotReduzidaMaxKva     float64 `json:"potReduzidaMaxKva"`     // potência máx. para IVA reduzido no termo de potência
	GasIVA                float64 `json:"gasIva"`                // %
	GasISPkWh             float64 `json:"gasIspKwh"`             // €/kWh
	GasDGEGMes            float64 `json:"gasDgegMes"`            // €/mês
}

// Request é o pedido de cálculo para UM fornecedor.
type Request struct {
	Tipo             string  `json:"tipo"` // "eletricidade" | "gas"
	Dias             int     `json:"dias"`
	PotenciaKva      float64 `json:"potenciaKva"`
	ConsumoKwh       float64 `json:"consumoKwh"`
	VazioPct         float64 `json:"vazioPct"`
	Tarifa           string  `json:"tarifa"` // "simples" | "bi"
	PotDia           float64 `json:"potDia"` // €/dia (potência ou termo fixo do gás)
	PrecoSimples     float64 `json:"precoSimples"`
	PrecoVazio       float64 `json:"precoVazio"`
	PrecoForaVazio   float64 `json:"precoForaVazio"`
	DescontoPct      float64 `json:"descontoPct"`
	DescontoPotencia bool    `json:"descontoPotencia"`
	OutrosMes        float64 `json:"outrosMes"`
	Fiscal           Fiscal  `json:"fiscal"`
}

// Line é uma componente da fatura.
type Line struct {
	Chave  string  `json:"chave"`
	Grupo  string  `json:"grupo"` // "antes" | "impostos" | "extra"
	Base   float64 `json:"base"`
	IVAPct float64 `json:"ivaPct"` // taxa efetiva
	IVA    float64 `json:"iva"`
	Total  float64 `json:"total"`
}

// Result é o resultado completo.
type Result struct {
	Linhas   []Line  `json:"linhas"`
	Subtotal float64 `json:"subtotal"` // antes de impostos (potência + energia)
	Impostos float64 `json:"impostos"` // IEC + DGEG + CAV (sem IVA)
	Extras   float64 `json:"extras"`
	IVA      float64 `json:"iva"`
	Total    float64 `json:"total"`
}

func r2(x float64) float64 { return math.Round(x*100) / 100 }

func pos(x float64) float64 {
	if x < 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return x
}

func clamp(x, lo, hi float64) float64 { return math.Min(math.Max(x, lo), hi) }

func linha(chave, grupo string, base, pct float64) Line {
	base = r2(base)
	iva := r2(base * pct / 100)
	return Line{Chave: chave, Grupo: grupo, Base: base, IVAPct: pct, IVA: iva, Total: r2(base + iva)}
}

// Calcular devolve o detalhe da fatura para um fornecedor.
func Calcular(q Request) Result {
	dias := float64(q.Dias)
	if dias < 1 {
		dias = 1
	}
	consumo := pos(q.ConsumoKwh)
	desc := clamp(q.DescontoPct, 0, 100)
	f := q.Fiscal

	var linhas []Line

	if q.Tipo == "gas" {
		termo := pos(q.PotDia) * dias
		energia := consumo * pos(q.PrecoSimples) * (1 - desc/100)
		linhas = append(linhas,
			linha("potencia", "antes", termo, f.GasIVA),
			linha("energia", "antes", energia, f.GasIVA),
			linha("iec", "impostos", consumo*pos(f.GasISPkWh), f.GasIVA),
			linha("dgeg", "impostos", pos(f.GasDGEGMes), f.GasIVA),
		)
	} else {
		// Potência
		potPct := f.IVANormal
		if q.PotenciaKva <= f.PotReduzidaMaxKva {
			potPct = f.IVAReduzido
		}
		potBruta := pos(q.PotDia) * dias
		if q.DescontoPotencia {
			potBruta *= 1 - desc/100
		}
		pot := linha("potencia", "antes", potBruta, potPct)

		// Energia
		var bruta float64
		if q.Tarifa == "bi" {
			v := clamp(q.VazioPct, 0, 100) / 100
			bruta = consumo*v*pos(q.PrecoVazio) + consumo*(1-v)*pos(q.PrecoForaVazio)
		} else {
			bruta = consumo * pos(q.PrecoSimples)
		}
		base := r2(bruta * (1 - desc/100))
		frac := 0.0
		if q.PotenciaKva <= f.EnergiaReduzidaMaxKva && consumo > 0 {
			frac = math.Min(consumo, pos(f.LimiteKwhReduzido)*dias/30) / consumo
		}
		enPct := frac*f.IVAReduzido + (1-frac)*f.IVANormal
		en := linha("energia", "antes", base, enPct)

		linhas = append(linhas, pot, en,
			linha("iec", "impostos", consumo*pos(f.IECkWh), f.IVANormal),
			linha("dgeg", "impostos", pos(f.DGEGMes), f.IVANormal),
		)
		if f.CAVAplica {
			linhas = append(linhas, linha("cav", "impostos", pos(f.CAVMes), f.IVAReduzido))
		}
	}

	if q.OutrosMes > 0 {
		pct := f.IVANormal
		if q.Tipo == "gas" {
			pct = f.GasIVA
		}
		linhas = append(linhas, linha("outros", "extra", pos(q.OutrosMes), pct))
	}

	var res Result
	res.Linhas = linhas
	var soma float64
	for _, l := range linhas {
		switch l.Grupo {
		case "antes":
			res.Subtotal += l.Base
		case "impostos":
			res.Impostos += l.Base
		default:
			res.Extras += l.Base
		}
		res.IVA += l.IVA
		soma += l.Base
	}
	res.Subtotal = r2(res.Subtotal)
	res.Impostos = r2(res.Impostos)
	res.Extras = r2(res.Extras)
	res.IVA = r2(res.IVA)
	res.Total = r2(soma + res.IVA)
	return res
}

// CalcularJSON é a fronteira JSON usada pela API: recebe e devolve JSON.
func CalcularJSON(entrada string) string {
	var q Request
	if err := json.Unmarshal([]byte(entrada), &q); err != nil {
		b, _ := json.Marshal(map[string]string{"erro": err.Error()})
		return string(b)
	}
	b, _ := json.Marshal(Calcular(q))
	return string(b)
}

// ======================= INTERFACE EMBUTIDA =======================
// (as crases do JavaScript estão escritas como "§" e convertidas em montar())

const indexHTML = `<!DOCTYPE html>
<html lang="pt-PT">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Simulador de Poupança Energética · Portugal</title>
<meta name="description" content="Compare faturas de eletricidade e gás natural em tempo real e apresente propostas de poupança aos clientes.">
<meta name="theme-color" content="#0f2a43">
<link rel="manifest" href="manifest.webmanifest">
<link rel="icon" href="icon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="icon-192.png">
<link rel="stylesheet" href="style.css">
</head>
<body>

<header class="topo">
  <div class="marca">
    <svg class="marca-ico" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="8" fill="currentColor"/><path d="M18 4 8 18h6l-2 10 10-14h-6z" fill="#fff"/></svg>
    <div>
      <h1>Simulador de poupança energética</h1>
      <p>Eletricidade e gás natural · Portugal</p>
    </div>
  </div>
  <div class="topo-acoes">
    <span class="pill" id="pill-rede" title="Estado da ligação">online</span>
    <span class="pill pill-motor" id="pill-motor" title="Motor de cálculo">a iniciar…</span>
    <button class="btn btn-ghost" id="btn-limpar" type="button" title="Apaga os dados do cliente, da fatura e da proposta">Limpar dados</button>
    <button class="btn btn-ghost" id="btn-tema" type="button" aria-label="Alternar tema claro/escuro">Tema</button>
  </div>
</header>

<nav class="separadores" role="tablist" aria-label="Tipo de energia">
  <button role="tab" class="sep ativo" data-modo="eletricidade" aria-selected="true">Eletricidade</button>
  <button role="tab" class="sep" data-modo="gas" aria-selected="false">Gás natural</button>
  <button role="tab" class="sep" data-modo="ambos" aria-selected="false">Eletricidade + Gás</button>
</nav>

<main class="layout">

  <!-- ENTRADAS -->
  <section class="coluna-entradas" aria-label="Dados da simulação">

    <div class="bloco bloco-fatura">
      <div class="bloco-cab">
        <h2>Fatura do cliente</h2>
        <span class="pill pill-motor" id="pill-leitura">a verificar…</span>
      </div>
      <label class="dropzone desativada" id="dropzone">
        <input type="file" id="ficheiro-fatura" accept=".pdf,.png,.jpg,.jpeg,.webp,.gif,.docx,.xlsx,.txt,.csv,.html,image/*">
        <strong>Carregue a fatura do cliente</strong>
        <span>Arraste, toque para escolher ou cole uma captura · PDF, foto, Word, Excel ou texto</span>
      </label>
      <p class="nota-priv" id="nota-ocr" hidden></p>
      <p class="nota-priv" id="nota-priv" hidden>Na leitura por IA o ficheiro é enviado à API da Anthropic para extrair os dados. Informe o cliente e não guarde faturas sem necessidade.</p>
      <details class="chave-ia" id="chave-ia" hidden>
        <summary>Chave da API (opcional · leitura por IA) <span id="chave-estado"></span></summary>
        <div class="chave-linha">
          <input type="password" id="chave-input" autocomplete="off" spellcheck="false" placeholder="Cole aqui a chave e clique em Guardar" aria-label="Chave da API Anthropic">
          <button class="btn" id="btn-guardar-chave" type="button">Guardar</button>
          <button class="btn btn-ghost" id="btn-remover-chave" type="button" hidden>Remover</button>
        </div>
        <p class="nota-priv" id="chave-nota"></p>
      </details>
      <details class="chave-ia" id="ferramentas-ia" hidden>
        <summary>Ferramentas de leitura local <span id="ferr-estado"></span></summary>
        <div class="chave-linha"><input type="text" id="ferr-poppler" placeholder="Pasta do pdftotext, ex.: C:\Program Files\Git\mingw32\bin" spellcheck="false" aria-label="Pasta do pdftotext"></div>
        <div class="chave-linha"><input type="text" id="ferr-tesseract" placeholder="Pasta do Tesseract, ex.: C:\Program Files\Tesseract-OCR" spellcheck="false" aria-label="Pasta do Tesseract"></div>
        <div class="chave-linha"><button class="btn" id="btn-guardar-ferr" type="button">Guardar caminhos</button></div>
        <p class="nota-priv" id="ferr-nota"></p>
      </details>
      <div class="leitura" id="leitura" aria-live="polite"></div>
    </div>

    <div class="bloco bloco-cliente">
      <h2>Cliente e proposta</h2>
      <div class="grelha g2">
        <label class="campo">Nome do cliente
          <input type="text" data-s="meta" data-k="cliente" placeholder="Ex.: Maria Santos">
        </label>
        <label class="campo">Consultor / empresa
          <input type="text" data-s="meta" data-k="consultor" placeholder="Ex.: A sua consultora">
        </label>
      </div>
    </div>

    <div class="bloco" id="bloco-perfil">
      <div class="bloco-cab">
        <h2>Perfil de consumo</h2>
        <button class="btn btn-link" id="btn-exemplo" type="button">Carregar exemplo</button>
      </div>
      <div class="grelha g2">
        <label class="campo" data-so="eletricidade">Potência contratada
          <select data-s="perfil" data-k="potenciaKva" data-num>
            <option value="">—</option><option>1.15</option><option>2.3</option><option>3.45</option><option selected>4.6</option>
            <option>5.75</option><option>6.9</option><option>10.35</option><option>13.8</option>
            <option>17.25</option><option>20.7</option><option>27.6</option><option>34.5</option><option>41.4</option>
          </select>
          <span class="un">kVA</span>
        </label>
        <label class="campo">Consumo no período
          <input type="number" inputmode="decimal" min="0" step="any" data-s="perfil" data-k="consumoKwh" data-num>
          <span class="un">kWh</span>
        </label>
        <label class="campo">Dias de faturação
          <input type="number" inputmode="numeric" min="1" max="366" step="1" data-s="perfil" data-k="dias" data-num data-int>
          <span class="un">dias</span>
        </label>
        <label class="campo" data-so="gas">Escalão de consumo
          <select data-s="perfil" data-k="escalao" data-num>
            <option value="">—</option><option>1</option><option>2</option><option>3</option><option>4</option>
          </select>
        </label>
        <label class="campo" data-so="eletricidade">Tarifário
          <select data-s="perfil" data-k="tarifa">
            <option value="simples">Simples</option>
            <option value="bi">Bi-horário</option>
          </select>
        </label>
        <label class="campo p-bi" data-so="eletricidade" hidden>Consumo em vazio
          <input type="number" inputmode="decimal" min="0" max="100" step="1" data-s="perfil" data-k="vazioPct" data-num>
          <span class="un">%</span>
        </label>
      </div>
    </div>

    <div class="duas-cols" id="bloco-fornecedores">
      <div class="bloco bloco-atual">
        <h2>Fornecedor atual</h2>
        <label class="campo">Nome
          <input type="text" data-s="atual" data-k="nome">
        </label>
        <div class="grelha g2">
          <label class="campo"><span class="rot-pot">Potência</span>
            <input type="number" inputmode="decimal" min="0" step="any" data-s="atual" data-k="potDia" data-num>
            <span class="un">€/dia</span>
          </label>
          <label class="campo p-simples"><span class="rot-en">Energia</span>
            <input type="number" inputmode="decimal" min="0" step="any" data-s="atual" data-k="precoSimples" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo p-bi" hidden>Vazio
            <input type="number" inputmode="decimal" min="0" step="any" data-s="atual" data-k="precoVazio" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo p-bi" hidden>Fora do vazio
            <input type="number" inputmode="decimal" min="0" step="any" data-s="atual" data-k="precoForaVazio" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo">Desconto
            <input type="number" inputmode="decimal" min="0" max="100" step="any" data-s="atual" data-k="descontoPct" data-num>
            <span class="un">%</span>
          </label>
          <label class="campo check largo" data-so="eletricidade"><input type="checkbox" data-s="atual" data-k="descontoPotencia"> Desconto também na potência</label>
          <label class="campo">Outros serviços
            <input type="number" inputmode="decimal" min="0" step="any" data-s="atual" data-k="outrosMes" data-num>
            <span class="un">€/mês</span>
          </label>
        </div>
      </div>

      <div class="bloco bloco-proposta">
        <h2>Proposta</h2>
        <label class="campo">Nome
          <input type="text" data-s="proposta" data-k="nome">
        </label>
        <div class="grelha g2">
          <label class="campo"><span class="rot-pot">Potência</span>
            <input type="number" inputmode="decimal" min="0" step="any" data-s="proposta" data-k="potDia" data-num>
            <span class="un">€/dia</span>
          </label>
          <label class="campo p-simples"><span class="rot-en">Energia</span>
            <input type="number" inputmode="decimal" min="0" step="any" data-s="proposta" data-k="precoSimples" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo p-bi" hidden>Vazio
            <input type="number" inputmode="decimal" min="0" step="any" data-s="proposta" data-k="precoVazio" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo p-bi" hidden>Fora do vazio
            <input type="number" inputmode="decimal" min="0" step="any" data-s="proposta" data-k="precoForaVazio" data-num>
            <span class="un">€/kWh</span>
          </label>
          <label class="campo">Desconto
            <input type="number" inputmode="decimal" min="0" max="100" step="any" data-s="proposta" data-k="descontoPct" data-num>
            <span class="un">%</span>
          </label>
          <label class="campo check largo" data-so="eletricidade"><input type="checkbox" data-s="proposta" data-k="descontoPotencia"> Desconto também na potência</label>
          <label class="campo">Outros serviços
            <input type="number" inputmode="decimal" min="0" step="any" data-s="proposta" data-k="outrosMes" data-num>
            <span class="un">€/mês</span>
          </label>
        </div>
      </div>
    </div>

    <div class="bloco bloco-tabela" id="bloco-tabela-elec" data-vista="eletricidade">
      <div class="bloco-cab">
        <h2>Tabela de preços da proposta</h2>
        <span class="pill pill-motor" id="tabela-estado">sem tabela</span>
      </div>
      <label class="dropzone desativada" id="dropzone-tabela">
        <input type="file" id="ficheiro-tabela" accept=".pdf,.png,.jpg,.jpeg,.webp,.gif,.tif,.tiff,.bmp,.docx,.xlsx,.odt,.ods,.rtf,.txt,.csv,.html,image/*">
        <strong>Carregue a tabela de preços ou a fatura da outra concessionária</strong>
        <span>PDF, foto, Word, Excel ou texto · tabela por potência (kVA) ou fatura com os preços</span>
      </label>
      <div class="leitura" id="leitura-tabela" aria-live="polite"></div>
      <div class="grelha g3 tab-global">
        <label class="campo">Concessionária <input type="text" id="tg-nome" placeholder="Nome da concessionária" autocomplete="off"></label>
        <label class="campo">Energia simples <input type="number" inputmode="decimal" step="any" min="0" id="tg-simples" data-tg="simples"><span class="un">€/kWh</span></label>
        <label class="campo">Vazio <input type="number" inputmode="decimal" step="any" min="0" id="tg-vazio" data-tg="vazio"><span class="un">€/kWh</span></label>
        <label class="campo">Fora de vazio <input type="number" inputmode="decimal" step="any" min="0" id="tg-fora" data-tg="fora"><span class="un">€/kWh</span></label>
        <label class="campo">Desconto <input type="number" inputmode="decimal" step="any" min="0" max="100" id="tg-desc" data-tg="descontoPct"><span class="un">%</span></label>
      </div>
      <div class="tabela-wrap">
        <table class="tabela tabela-prop">
          <caption class="sr-only">Preços da proposta por potência contratada</caption>
          <thead><tr><th scope="col">kVA</th><th scope="col">Potência €/dia</th><th scope="col">Simples €/kWh</th><th scope="col">Vazio €/kWh</th><th scope="col">Fora vazio €/kWh</th><th scope="col"></th></tr></thead>
          <tbody id="tabela-corpo"></tbody>
        </table>
      </div>
      <div class="acoes">
        <button class="btn" id="btn-add-linha" type="button">Adicionar linha</button>
        <button class="btn btn-ghost" id="btn-limpar-tabela" type="button">Limpar tabela</button>
      </div>
      <p class="nota-priv" id="tabela-uso"></p>
    </div>

    <div class="bloco bloco-tabela" id="bloco-tabela-gas" data-vista="gas">
      <div class="bloco-cab">
        <h2>Tabela de preços do gás da proposta</h2>
        <span class="pill pill-motor" id="tabela-estado-gas">sem tabela</span>
      </div>
      <label class="dropzone desativada" id="dropzone-tabela-gas">
        <input type="file" id="ficheiro-tabela-gas" accept=".pdf,.png,.jpg,.jpeg,.webp,.gif,.tif,.tiff,.bmp,.docx,.xlsx,.odt,.ods,.rtf,.txt,.csv,.html,image/*">
        <strong>Carregue a tabela de gás ou a fatura da outra concessionária</strong>
        <span>PDF, foto, Word, Excel ou texto · tabela por escalão ou fatura com os preços</span>
      </label>
      <div class="leitura" id="leitura-tabela-gas" aria-live="polite"></div>
      <div class="grelha g3 tab-global">
        <label class="campo">Concessionária <input type="text" id="tgg-nome" placeholder="Nome da concessionária" autocomplete="off"></label>
        <label class="campo">Termo fixo <input type="number" inputmode="decimal" step="any" min="0" id="tgg-termo" data-tgg="termo"><span class="un">€/dia</span></label>
        <label class="campo">Energia <input type="number" inputmode="decimal" step="any" min="0" id="tgg-preco" data-tgg="preco"><span class="un">€/kWh</span></label>
        <label class="campo">Desconto <input type="number" inputmode="decimal" step="any" min="0" max="100" id="tgg-desc" data-tgg="descontoPct"><span class="un">%</span></label>
      </div>
      <div class="tabela-wrap">
        <table class="tabela tabela-prop">
          <caption class="sr-only">Preços do gás da proposta por escalão de consumo</caption>
          <thead><tr><th scope="col">Escalão</th><th scope="col">Termo fixo €/dia</th><th scope="col">Energia €/kWh</th><th scope="col"></th></tr></thead>
          <tbody id="tabela-corpo-gas"></tbody>
        </table>
      </div>
      <div class="acoes">
        <button class="btn" id="btn-add-linha-gas" type="button">Adicionar linha</button>
        <button class="btn btn-ghost" id="btn-limpar-tabela-gas" type="button">Limpar tabela</button>
      </div>
      <p class="nota-priv" id="tabela-uso-gas"></p>
    </div>

    <details class="bloco fiscal">
      <summary>Parâmetros fiscais e taxas legais <small>editáveis · confirme os valores em vigor</small></summary>
      <div class="grelha g3" id="fiscal-elec" data-vista="eletricidade">
        <label class="campo">IEC <input type="number" step="any" data-s="fiscal" data-k="iecKwh" data-num><span class="un">€/kWh</span></label>
        <label class="campo">Taxa DGEG <input type="number" step="any" data-s="fiscal" data-k="dgegMes" data-num><span class="un">€/mês</span></label>
        <label class="campo">Contribuição audiovisual <input type="number" step="any" data-s="fiscal" data-k="cavMes" data-num><span class="un">€/mês</span></label>
        <label class="campo">IVA normal <input type="number" step="any" data-s="fiscal" data-k="ivaNormal" data-num><span class="un">%</span></label>
        <label class="campo">IVA reduzido <input type="number" step="any" data-s="fiscal" data-k="ivaReduzido" data-num><span class="un">%</span></label>
        <label class="campo">kWh c/ IVA reduzido <input type="number" step="any" data-s="fiscal" data-k="limiteKwhReduzido" data-num><span class="un">kWh/30d</span></label>
        <label class="campo">Energia reduzida até <input type="number" step="any" data-s="fiscal" data-k="energiaReduzidaMaxKva" data-num><span class="un">kVA</span></label>
        <label class="campo">Potência reduzida até <input type="number" step="any" data-s="fiscal" data-k="potReduzidaMaxKva" data-num><span class="un">kVA</span></label>
        <label class="campo check"><input type="checkbox" data-s="fiscal" data-k="cavAplica"> Cobrar CAV</label>
      </div>
      <div class="grelha g3" id="fiscal-gas" data-vista="gas" hidden>
        <label class="campo">IVA gás <input type="number" step="any" data-s="fiscal" data-k="gasIva" data-num><span class="un">%</span></label>
        <label class="campo">Imposto s/ gás (ISP) <input type="number" step="any" data-s="fiscal" data-k="gasIspKwh" data-num><span class="un">€/kWh</span></label>
        <label class="campo">Taxa DGEG gás <input type="number" step="any" data-s="fiscal" data-k="gasDgegMes" data-num><span class="un">€/mês</span></label>
      </div>
      <button class="btn btn-link" id="btn-repor-fiscal" type="button">Repor valores por omissão</button>
    </details>
  </section>

  <!-- RESULTADOS -->
  <aside class="coluna-resultado" aria-live="polite">

    <div class="print-cab">
      <div>
        <strong id="p-consultor">—</strong>
        <span>Proposta de poupança energética</span>
      </div>
      <div class="print-dados">
        <span>Cliente: <b id="p-cliente">—</b></span>
        <span id="p-data"></span>
      </div>
    </div>

    <div class="heroi" id="heroi">
      <p class="heroi-rot" id="heroi-rot">Poupança por fatura</p>
      <p class="heroi-num" id="heroi-num">0,00 €</p>
      <p class="heroi-sub" id="heroi-sub">—</p>
      <div class="heroi-kpis">
        <div><span>Por ano</span><b id="kpi-ano">—</b></div>
        <div><span>Redução</span><b id="kpi-pct">—</b></div>
        <div><span>Em 24 meses</span><b id="kpi-24">—</b></div>
      </div>
    </div>

    <div class="barras" aria-hidden="true">
      <div class="barra-lin"><span id="b-nome-a">Atual</span><div class="barra"><i id="b-a"></i></div><b id="b-v-a">—</b></div>
      <div class="barra-lin"><span id="b-nome-p">Proposta</span><div class="barra"><i id="b-p" class="prop"></i></div><b id="b-v-p">—</b></div>
    </div>

    <div class="tabela-wrap">
      <table class="tabela" id="tabela">
        <thead>
          <tr><th>Componente</th><th class="num" id="th-a">Atual</th><th class="num" id="th-p">Proposta</th><th class="num">Poupança</th></tr>
        </thead>
        <tbody id="tbody"></tbody>
      </table>
    </div>

    <p class="nota-sub"><strong>O subtotal não é o total da fatura.</strong> Ainda faltam impostos, taxas e eventuais serviços adicionais.</p>

    <div class="acoes">
      <button class="btn btn-primario" id="btn-imprimir" type="button">Gerar proposta (PDF)</button>
      <button class="btn" id="btn-copiar" type="button">Copiar resumo</button>
    </div>
    <p class="aviso-print">Valores estimativos, calculados com os preços e as taxas indicados nesta proposta. A fatura real depende do consumo efetivo, de acertos de leitura e da legislação em vigor.</p>
  </aside>
</main>

<section class="explica" aria-label="Como é calculada uma fatura">
  <h2>Como é calculada uma fatura de eletricidade em Portugal</h2>
  <div class="tabela-wrap">
  <table class="tabela tabela-info">
    <thead><tr><th>Componente</th><th>Como é calculada</th></tr></thead>
    <tbody>
      <tr><td>Potência contratada</td><td>Preço diário × número de dias</td></tr>
      <tr><td>Energia consumida</td><td>kWh consumidos × preço por kWh</td></tr>
      <tr><td>IEC</td><td>Imposto especial de consumo de eletricidade</td></tr>
      <tr><td>Taxa DGEG</td><td>Taxa fixa de exploração aplicável</td></tr>
      <tr><td>Contribuição audiovisual (CAV)</td><td>Valor mensal, quando aplicável</td></tr>
      <tr><td>IVA</td><td>Aplicado às componentes conforme as taxas legais</td></tr>
    </tbody>
  </table>
  </div>
</section>

<footer class="rodape">
  <p>Ferramenta de apoio à consultoria. Os parâmetros fiscais são editáveis e devem ser confirmados nas fontes oficiais (ERSE, DGEG, Autoridade Tributária) antes de cada proposta.</p>
</footer>

<div class="barra-fixa" id="barra-fixa" aria-hidden="true">
  <span>Poupança por fatura</span><b id="fixa-val">—</b>
</div>
<div class="toast" id="toast" role="status"></div>

<script src="engine.js"></script>
<script src="app.js"></script>
</body>
</html>
`

const styleCSS = `:root{
  --ink:#0f2a43; --ink-2:#38506a; --muted:#6a7c8f;
  --bg:#eef1f5; --card:#ffffff; --line:#dbe1e8; --field:#f7f9fb;
  --prop:#0b8f63; --prop-bg:#e6f5ef; --atual:#7a8da3; --atual-bg:#eef1f5;
  --neg:#c2410c; --focus:#1f6feb;
  --hero-bg:#0f2a43; --hero-ink:#ffffff; --hero-mut:#9fb4c9;
  --r:12px;
  --font: "Segoe UI", system-ui, -apple-system, "Helvetica Neue", Arial, sans-serif;
}
:root[data-tema="escuro"]{
  --ink:#e8eef5; --ink-2:#b4c2d1; --muted:#8497ab;
  --bg:#0b1522; --card:#122234; --line:#233750; --field:#0e1c2c;
  --prop:#35c28f; --prop-bg:#12342b; --atual:#8da2b8; --atual-bg:#1a2c42;
  --neg:#f08a5d; --hero-bg:#0a3a2f; --hero-ink:#fff; --hero-mut:#a9d9c8;
}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 var(--font);padding-bottom:72px}
h1,h2{margin:0;line-height:1.2}
h1{font-size:1.15rem;font-weight:700;letter-spacing:-.01em}
h2{font-size:1rem;font-weight:650}
:focus-visible{outline:3px solid var(--focus);outline-offset:2px}

/* Topo */
.topo{display:flex;flex-wrap:wrap;gap:12px;align-items:center;justify-content:space-between;
  padding:14px 20px;background:var(--card);border-bottom:1px solid var(--line);
  padding-top:max(14px,env(safe-area-inset-top))}
.marca{display:flex;gap:12px;align-items:center}
.marca-ico{width:38px;height:38px;color:var(--ink)}
.marca p{margin:0;color:var(--muted);font-size:.82rem}
.topo-acoes{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.pill{font-size:.75rem;padding:3px 10px;border-radius:99px;background:var(--prop-bg);color:var(--prop);font-weight:600}
.pill.off{background:#fde8e1;color:var(--neg)}
.pill-motor{background:var(--atual-bg);color:var(--ink-2)}

/* Botões */
.btn{font:inherit;font-weight:600;border:1px solid var(--line);background:var(--card);color:var(--ink);
  padding:9px 16px;border-radius:9px;cursor:pointer;min-height:40px}
.btn:hover{border-color:var(--ink-2)}
.btn-primario{background:var(--ink);color:var(--card);border-color:var(--ink)}
:root[data-tema="escuro"] .btn-primario{background:var(--prop);color:#06241a;border-color:var(--prop)}
.btn-ghost{background:transparent}
.btn-link{border:0;background:none;color:var(--focus);padding:4px 0;min-height:0;text-decoration:underline;text-underline-offset:3px}

/* Separadores */
.separadores{display:flex;gap:4px;padding:14px 20px 0;max-width:1240px;margin:0 auto}
.sep{font:inherit;font-weight:600;border:0;background:transparent;color:var(--muted);padding:10px 18px;
  border-radius:10px 10px 0 0;cursor:pointer;border-bottom:3px solid transparent}
.sep.ativo{color:var(--ink);border-bottom-color:var(--prop)}

/* Layout */
.layout{display:grid;grid-template-columns:minmax(0,1.05fr) minmax(0,1fr);gap:20px;max-width:1240px;margin:0 auto;padding:16px 20px}
.coluna-entradas{display:flex;flex-direction:column;gap:16px;min-width:0}
.coluna-resultado{position:sticky;top:12px;align-self:start;display:flex;flex-direction:column;gap:14px;min-width:0}
.bloco{background:var(--card);border:1px solid var(--line);border-radius:var(--r);padding:18px}
.bloco h2{margin-bottom:12px}
.bloco-cab{display:flex;justify-content:space-between;align-items:baseline;margin-bottom:12px}
.bloco-cab h2{margin:0}
.duas-cols{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.bloco-atual{border-top:4px solid var(--atual)}
.bloco-proposta{border-top:4px solid var(--prop)}
.grelha{display:grid;gap:12px;align-items:end}
.g2{grid-template-columns:1fr 1fr}
.g3{grid-template-columns:repeat(3,1fr)}
.bloco>.campo{margin-bottom:12px}

/* Campos */
.campo{display:flex;flex-direction:column;gap:5px;font-size:.82rem;color:var(--ink-2);font-weight:600;position:relative;min-width:0}
.campo[hidden],[hidden]{display:none!important}
.campo input,.campo select{font:inherit;font-size:1rem;font-weight:500;color:var(--ink);background:var(--field);
  border:1px solid var(--line);border-radius:9px;padding:10px 46px 10px 12px;width:100%;min-height:44px;font-variant-numeric:tabular-nums}
.campo input[type=text]{padding-right:12px}
.campo select{padding-right:12px}
.campo .un{position:absolute;right:11px;bottom:12px;font-size:.75rem;color:var(--muted);font-weight:500;pointer-events:none}
.campo select + .un{right:30px}
.campo.check{flex-direction:row;align-items:center;gap:8px;align-self:end;min-height:44px}
.campo.check input{width:20px;min-height:20px;padding:0}

.fiscal summary{cursor:pointer;font-weight:650;list-style:none}
.fiscal summary small{display:block;color:var(--muted);font-weight:500;margin-top:2px}
.fiscal .grelha{margin:14px 0}

/* Herói */
.heroi{background:var(--hero-bg);color:var(--hero-ink);border-radius:var(--r);padding:22px}
.heroi-rot{margin:0;color:var(--hero-mut);font-size:.85rem}
.heroi-num{margin:2px 0 4px;font-size:clamp(2.4rem,6vw,3.4rem);font-weight:750;letter-spacing:-.03em;line-height:1.05;font-variant-numeric:tabular-nums}
.heroi-sub{margin:0 0 16px;color:var(--hero-mut);font-size:.88rem}
.heroi-kpis{display:grid;grid-template-columns:repeat(3,1fr);gap:10px;border-top:1px solid rgba(255,255,255,.18);padding-top:14px}
.heroi-kpis span{display:block;color:var(--hero-mut);font-size:.75rem}
.heroi-kpis b{font-size:1.1rem;font-variant-numeric:tabular-nums}
.heroi.neg{background:#5b2a14}

/* Barras */
.barras{background:var(--card);border:1px solid var(--line);border-radius:var(--r);padding:14px 16px;display:flex;flex-direction:column;gap:10px}
.barra-lin{display:grid;grid-template-columns:96px 1fr 84px;gap:10px;align-items:center;font-size:.85rem}
.barra-lin span{color:var(--ink-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.barra-lin b{text-align:right;font-variant-numeric:tabular-nums}
.barra{height:14px;background:var(--atual-bg);border-radius:7px;overflow:hidden}
.barra i{display:block;height:100%;width:0;background:var(--atual);border-radius:7px;transition:width .35s ease}
.barra i.prop{background:var(--prop)}

/* Tabela */
.tabela-wrap{background:var(--card);border:1px solid var(--line);border-radius:var(--r);overflow-x:auto}
.tabela{width:100%;border-collapse:collapse;font-size:.88rem;font-variant-numeric:tabular-nums}
.tabela th,.tabela td{padding:9px 12px;text-align:left;border-bottom:1px solid var(--line)}
.tabela th{font-size:.78rem;color:var(--muted);font-weight:650;background:var(--field)}
.tabela .num{text-align:right;white-space:nowrap}
.tabela td small{display:block;color:var(--muted);font-size:.74rem}
.tabela tr.sub td{background:var(--field);font-weight:700}
.tabela tr.tot td{font-weight:750;font-size:1rem;border-bottom:0;background:var(--prop-bg)}
.tabela .bom{color:var(--prop);font-weight:650}
.tabela .mau{color:var(--neg);font-weight:650}
.tabela tr:last-child td{border-bottom:0}
.nota-sub{margin:0;font-size:.82rem;color:var(--muted)}
.acoes{display:flex;gap:10px;flex-wrap:wrap}
.acoes .btn{flex:1 1 150px}
.aviso-print,.print-cab{display:none}

.explica{max-width:1240px;margin:8px auto 0;padding:0 20px}
.explica h2{margin-bottom:10px}
.tabela-info td:first-child{font-weight:650;width:38%}
.rodape{max-width:1240px;margin:20px auto 0;padding:0 20px 30px;color:var(--muted);font-size:.8rem}

/* Barra fixa (telemóvel) */
.barra-fixa{display:none;position:fixed;left:0;right:0;bottom:0;background:var(--hero-bg);color:#fff;
  padding:12px 20px calc(12px + env(safe-area-inset-bottom));justify-content:space-between;align-items:center;z-index:5}
.barra-fixa span{font-size:.85rem;color:var(--hero-mut)}
.barra-fixa b{font-size:1.25rem;font-variant-numeric:tabular-nums}
.toast{position:fixed;left:50%;bottom:90px;transform:translate(-50%,20px);background:var(--ink);color:var(--card);
  padding:10px 16px;border-radius:9px;opacity:0;pointer-events:none;transition:.25s;z-index:10;font-size:.9rem}
.toast.ver{opacity:1;transform:translate(-50%,0)}

@media (max-width:980px){
  .layout{grid-template-columns:1fr}
  .coluna-resultado{position:static}
  .barra-fixa{display:flex}
}
@media (max-width:620px){
  .duas-cols,.g3{grid-template-columns:1fr}
  .topo,.layout,.separadores,.explica,.rodape{padding-left:14px;padding-right:14px}
  .barra-lin{grid-template-columns:76px 1fr 76px}
}
@media (prefers-reduced-motion:reduce){*{transition:none!important}}

/* Impressão: só a proposta */
@media print{
  :root{--bg:#fff;--card:#fff;--hero-bg:#0f2a43}
  *{-webkit-print-color-adjust:exact;print-color-adjust:exact}
  body{background:#fff;padding:0;font-size:12px}
  .topo,.separadores,.coluna-entradas,.acoes,.barra-fixa,.toast,.rodape,.explica,.nota-sub{display:none!important}
  .layout{display:block;padding:0;margin:0}
  .coluna-resultado{position:static}
  .print-cab{display:flex;justify-content:space-between;align-items:flex-end;border-bottom:3px solid var(--ink);padding-bottom:10px;margin-bottom:6px}
  .print-cab strong{display:block;font-size:1.3rem}
  .print-cab span{color:var(--muted)}
  .print-dados{text-align:right;display:flex;flex-direction:column}
  .aviso-print{display:block;font-size:.72rem;color:var(--muted);margin:4px 0 0}
  .tabela-wrap,.barras,.heroi{break-inside:avoid}
}
@media (max-width:620px){ .bloco-cliente .g2{grid-template-columns:1fr} }

/* Leitura de faturas */
.bloco-fatura{border-top:4px solid var(--focus)}
.dropzone{position:relative;display:flex;flex-direction:column;gap:4px;align-items:center;text-align:center;
  border:2px dashed var(--line);border-radius:var(--r);padding:20px 14px;cursor:pointer;background:var(--field);color:var(--ink-2)}
.dropzone strong{color:var(--ink)}
.dropzone span{font-size:.82rem}
.dropzone:hover,.dropzone.sobre{border-color:var(--prop);background:var(--prop-bg)}
.dropzone:focus-within{outline:3px solid var(--focus);outline-offset:2px}
.dropzone input{position:absolute;opacity:0;width:1px;height:1px;pointer-events:none}
.dropzone.desativada{opacity:.65}
.nota-priv{margin:8px 0 0;font-size:.76rem;color:var(--muted)}
.leitura{margin-top:12px;font-size:.88rem}
.leitura-ok,.leitura-erro,.leitura-aguarde{border-radius:10px;padding:12px 14px}
.leitura-ok{background:var(--prop-bg)}
.leitura-aguarde{background:var(--atual-bg)}
.leitura-erro{background:#fde8e1;color:#7a2e0e}
:root[data-tema="escuro"] .leitura-erro{background:#3a1d12;color:#f3b79d}
.leitura p{margin:8px 0 0}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0 0}
.chip{display:inline-block;background:var(--card);border:1px solid var(--line);border-radius:99px;padding:2px 10px;font-size:.78rem;font-variant-numeric:tabular-nums;color:var(--ink)}
.campo input.lido,.campo select.lido{border-color:var(--prop);box-shadow:0 0 0 3px var(--prop-bg)}

.chave-ia{margin-top:10px;font-size:.85rem}
.chave-ia summary{cursor:pointer;font-weight:600;color:var(--ink-2)}
.chave-ia summary span{font-weight:500;color:var(--muted)}
.chave-linha{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap}
/* Tabela de preços da proposta */
.bloco-tabela{border-top:4px solid var(--prop)}
.tab-global{margin:12px 0}
.tabela-prop{min-width:560px}
.tabela-prop th,.tabela-prop td{padding:6px 8px}
.tabela-prop input{width:100%;min-width:72px;font:inherit;color:var(--ink);background:var(--field);border:1px solid var(--line);border-radius:7px;padding:6px 8px;min-height:36px;font-variant-numeric:tabular-nums}
.tabela-prop tr.ativa td{background:var(--prop-bg)}
.tabela-prop .rm{border:0;background:none;color:var(--neg);cursor:pointer;font-size:1.2rem;min-height:36px;padding:0 8px}
.bloco-tabela .acoes{margin-top:10px}
.chave-linha input{flex:1 1 220px;min-width:0;font:inherit;color:var(--ink);background:var(--field);border:1px solid var(--line);border-radius:9px;padding:10px 12px;min-height:44px}

.texto-lido{max-height:220px;overflow:auto;white-space:pre-wrap;font-size:.74rem;background:var(--card);border:1px solid var(--line);border-radius:8px;padding:8px;margin:6px 0 0;color:var(--ink)}
.leitura details{margin-top:8px}
.leitura summary{cursor:pointer;font-weight:600}

.campo.check.largo{grid-column:1/-1;min-height:0}

/* Vista combinada e confirmação */
.tabela tr.grupo td{background:var(--atual-bg);color:var(--ink);font-weight:700;border-top:2px solid var(--line)}
.modal-fundo{position:fixed;inset:0;background:rgba(8,20,34,.55);display:flex;align-items:center;justify-content:center;z-index:50;padding:16px}
.modal{background:var(--card);color:var(--ink);border-radius:14px;padding:22px;max-width:380px;width:100%;box-shadow:0 20px 60px rgba(0,0,0,.35)}
.modal p{margin:0 0 18px;font-size:1.05rem;font-weight:600}
.modal-acoes{display:flex;gap:10px;justify-content:flex-end}
.modal-acoes .btn{min-width:90px}
`

const appJS = `/* Interface do simulador — cálculo em tempo real.
   Motor: Go (servidor) com fallback automático para JavaScript. */
(function () {
  "use strict";

  /* ---------- Valores por omissão ---------- */
  const FISCAL_DEFAULT = {
    iecKwh: 0.001, dgegMes: 0.07, cavMes: 2.85, cavAplica: true,
    ivaNormal: 23, ivaReduzido: 6, limiteKwhReduzido: 200,
    energiaReduzidaMaxKva: 6.9, potReduzidaMaxKva: 3.45,
    gasIva: 23, gasIspKwh: 0, gasDgegMes: 0.07,
  };
  const MODO_DEFAULT = () => ({
    eletricidade: {
      perfil: { potenciaKva: 4.6, consumoKwh: 200, dias: 30, tarifa: "simples", vazioPct: 40 },
      atual: { nome: "Concessionária atual", potDia: 0.30, precoSimples: 0.15, precoVazio: 0.11, precoForaVazio: 0.19, descontoPct: 0, descontoPotencia: false, outrosMes: 0 },
      proposta: { nome: "", potDia: null, precoSimples: null, precoVazio: null, precoForaVazio: null, descontoPct: null, descontoPotencia: false, outrosMes: null },
    },
    gas: {
      perfil: { consumoKwh: 350, dias: 30, tarifa: "simples", escalao: null },
      atual: { nome: "Fornecedor atual", potDia: 0.18, precoSimples: 0.075, descontoPct: 0, descontoPotencia: false, outrosMes: 0 },
      proposta: { nome: "", potDia: null, precoSimples: null, descontoPct: null, descontoPotencia: false, outrosMes: null },
    },
  });

  const TABELA_DEFAULT = () => ({ nome: "", linhas: [], global: { simples: null, vazio: null, fora: null, descontoPct: null } });

  const TABELA_GAS_DEFAULT = () => ({ nome: "", linhas: [], global: { termo: null, preco: null, descontoPct: null } });

  const CHAVE = "calc-energia-pt:v2";
  let state = carregar();

  function carregar() {
    const base = { vista: "eletricidade", tema: null, meta: { cliente: "", consultor: "" }, fiscal: { ...FISCAL_DEFAULT }, tabela: TABELA_DEFAULT(), tabelaGas: TABELA_GAS_DEFAULT(), ...MODO_DEFAULT() };
    try {
      const salvo = JSON.parse(localStorage.getItem(CHAVE) || "null");
      if (salvo) {
        for (const m of ["eletricidade", "gas"]) {
          if (!salvo[m]) continue;
          for (const s of ["perfil", "atual", "proposta"]) Object.assign(base[m][s], salvo[m][s] || {});
        }
        Object.assign(base.meta, salvo.meta || {});
        Object.assign(base.fiscal, salvo.fiscal || {});
        if (salvo.vista) base.vista = salvo.vista;
        base.tema = salvo.tema || null;
        if (salvo.tabelaGas) base.tabelaGas = { ...base.tabelaGas, ...salvo.tabelaGas, global: { ...base.tabelaGas.global, ...(salvo.tabelaGas.global || {}) } };
        if (salvo.tabela) base.tabela = { ...base.tabela, ...salvo.tabela, global: { ...base.tabela.global, ...(salvo.tabela.global || {}) } };
      }
    } catch (e) { /* storage indisponível */ }
    return base;
  }
  function gravar() { try { localStorage.setItem(CHAVE, JSON.stringify(state)); } catch (e) {} }

  /* ---------- Motor: servidor Go (API) com fallback JS ---------- */
  let motor = "js";
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];

  function calcularMotor(req) { return window.JSEngine.calcular(req); }

  async function calcularGo(req) {
    const r = await fetch("api/calcular", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(req) });
    if (!r.ok) throw new Error("http " + r.status);
    const j = await r.json();
    if (j.erro) throw new Error(j.erro);
    return j;
  }

  async function iniciarMotor() {
    const pill = $("#pill-motor");
    try {
      const r = await fetch("api/ping", { cache: "no-store" });
      if (!r.ok || (await r.text()).trim() !== "ok") throw new Error("sem servidor Go");
      motor = "api";
      pill.textContent = "motor Go · servidor";
      verificarLeitura();
    } catch (e) {
      motor = "js";
      pill.textContent = "motor JavaScript";
      desativarLeitura();
    }
    atualizar();
  }

  /* ---------- Formatação ---------- */
  const eur = new Intl.NumberFormat("pt-PT", { style: "currency", currency: "EUR" });
  const fmtE = (x) => eur.format(x);
  const fmtN = (x, d = 4) => Number(x).toLocaleString("pt-PT", { minimumFractionDigits: Math.min(d, 2), maximumFractionDigits: d });
  const fmtSinal = (x) => (Math.abs(x) < 0.005 ? "—" : (x > 0 ? "+" : "−") + eur.format(Math.abs(x)));

  /* ---------- Ligação UI ⇄ estado ---------- */
  const servicosDaVista = () => (state.vista === "ambos" ? ["eletricidade", "gas"] : [state.vista]);

  function modoDe(el) {
    const bl = el.closest("[data-m]");
    return bl ? bl.dataset.m : "eletricidade";
  }

  function objeto(el) {
    const s = el.dataset.s;
    if (s === "meta") return state.meta;
    if (s === "fiscal") return state.fiscal;
    return state[modoDe(el)][s];
  }

  function preencher() {
    garantirOpcaoKva();
    $$("[data-s][data-k]").forEach((el) => {
      const o = objeto(el);
      const v = o[el.dataset.k];
      if (v === undefined) return;
      if (el.type === "checkbox") el.checked = !!v;
      else el.value = v === null ? "" : v;
    });
    atualizarVisibilidade();
    destacar();
  }

  function atualizarVisibilidade() {
    const v = state.vista;
    $$("[data-vista]").forEach((el) => { el.hidden = !(v === "ambos" || el.dataset.vista === v); });
    $$("[data-m]").forEach((bl) => {
      const m = bl.dataset.m, gas = m === "gas";
      const bi = !gas && state.eletricidade.perfil.tarifa === "bi";
      bl.querySelectorAll("[data-so]").forEach((el) => { el.hidden = el.dataset.so !== m; });
      bl.querySelectorAll(".p-bi").forEach((el) => { el.hidden = !bi; });
      bl.querySelectorAll(".p-simples").forEach((el) => { el.hidden = bi; });
      bl.querySelectorAll(".rot-pot").forEach((el) => { el.textContent = gas ? "Termo fixo" : "Potência"; });
    });
    $$(".sep").forEach((b) => {
      const ativo = b.dataset.modo === v;
      b.classList.toggle("ativo", ativo);
      b.setAttribute("aria-selected", ativo);
    });
  }

  document.addEventListener("input", (ev) => {
    const el = ev.target;
    if (!el.dataset || !el.dataset.k) return;
    el.classList.remove("lido");
    const o = objeto(el);
    if (el.type === "checkbox") o[el.dataset.k] = el.checked;
    else if (el.hasAttribute("data-num")) {
      const n = el.hasAttribute("data-int") ? parseInt(String(el.value), 10) : parseFloat(String(el.value).replace(",", "."));
      o[el.dataset.k] = Number.isFinite(n) ? n : el.dataset.s === "proposta" || el.dataset.s === "atual" ? null : 0;
    } else o[el.dataset.k] = el.value;
    if (el.dataset.k === "tarifa") atualizarVisibilidade();
    if (el.dataset.s === "perfil" && (el.dataset.k === "potenciaKva" || el.dataset.k === "tarifa" || el.dataset.k === "escalao")) {
      aplicarTabela();
      aplicarTabelaGas();
      preencher();
    }
    atualizar();
  });

  /* ---------- Cálculo e renderização ---------- */
  function pedido(modo, quem) {
    const m = state[modo];
    return { tipo: modo, ...m.perfil, ...m[quem], fiscal: { ...state.fiscal } };
  }

  const ROTULOS = {
    eletricidade: { potencia: "Potência contratada", energia: "Energia consumida", iec: "IEC", dgeg: "Taxa DGEG", cav: "Contribuição audiovisual", outros: "Outros serviços" },
    gas: { potencia: "Termo fixo", energia: "Energia consumida", iec: "Imposto sobre o gás (ISP)", dgeg: "Taxa DGEG", outros: "Outros serviços" },
  };

  function detalhe(chave, quem, modo) {
    const m = state[modo], p = m.perfil, f = m[quem], fi = state.fiscal;
    switch (chave) {
      case "potencia": return §${fmtN(f.potDia)} €/dia × ${fmtN(p.dias, 0)} dias§;
      case "energia": {
        const d = f.descontoPct > 0 ? § − ${fmtN(f.descontoPct, 2)}%§ : "";
        if (modo === "eletricidade" && p.tarifa === "bi")
          return §${fmtN(p.consumoKwh, 1)} kWh (${fmtN(p.vazioPct, 0)}% vazio)${d}§;
        return §${fmtN(p.consumoKwh, 1)} × ${fmtN(f.precoSimples)} €/kWh${d}§;
      }
      case "iec": return modo === "gas" ? §${fmtN(p.consumoKwh, 1)} kWh × ${fmtN(fi.gasIspKwh)} €§ : §${fmtN(p.consumoKwh, 1)} kWh × ${fmtN(fi.iecKwh)} €§;
      case "dgeg": return "taxa de exploração";
      case "cav": return "valor mensal";
      default: return "";
    }
  }

  let ultimo = null;

  let seq = 0;
  function pedidos(modo) { return { a: pedido(modo, "atual"), p: pedido(modo, "proposta") }; }

  function atualizar() {
    const modos = servicosDaVista();
    // 1) resultado instantâneo (JS, funciona offline)
    const res = {};
    modos.forEach((m) => { const q = pedidos(m); res[m] = { a: calcularMotor(q.a), p: calcularMotor(q.p) }; });
    desenhar(res);
    // 2) confirmação pelo motor Go no servidor, quando disponível
    if (motor === "api") {
      const n = ++seq;
      Promise.all(modos.map((m) => {
        const q = pedidos(m);
        return Promise.all([calcularGo(q.a), calcularGo(q.p)]).then(([a2, p2]) => ({ m, a: a2, p: p2 }));
      })).then((lista) => {
        if (n !== seq) return;
        const r2 = {};
        lista.forEach((x) => { r2[x.m] = { a: x.a, p: x.p }; });
        desenhar(r2);
      }).catch(() => { /* offline: mantém o resultado JS */ });
    }
  }

  const NOME_SERVICO = { eletricidade: "Eletricidade", gas: "Gás natural" };
  const diasDe = (m) => Math.max(state[m].perfil.dias || 1, 1);
  const propostaVazia = (m) => {
    const q = state[m].proposta;
    return !(q.potDia > 0 || q.precoSimples > 0 || q.precoVazio > 0 || q.precoForaVazio > 0);
  };

  function desenhar(res) {
    ultimo = res;
    const modos = Object.keys(res);
    const ambos = modos.length > 1;
    const comp = modos.filter((m) => !propostaVazia(m)); // serviços com proposta preenchida
    const vazia = comp.length === 0;
    let totA = 0, totP = 0, poup = 0, ano = 0, todosA = 0;
    modos.forEach((m) => { todosA += res[m].a.total; });
    comp.forEach((m) => {
      const d = res[m].a.total - res[m].p.total;
      totA += res[m].a.total;
      totP += res[m].p.total;
      poup += d;
      ano += (d * 365) / diasDe(m);
    });
    const m0 = state[modos[0]];

    // Herói
    $("#heroi").classList.toggle("neg", !vazia && poup < -0.005);
    $("#heroi-rot").textContent = vazia ? "Comparação"
      : poup < -0.005 ? "A proposta é mais cara em"
      : ambos ? "Poupança por fatura (eletricidade + gás)" : "Poupança em " + fmtN(diasDe(modos[0]), 0) + " dias";
    $("#heroi-num").textContent = vazia ? "—" : fmtE(Math.abs(poup));
    $("#heroi-sub").textContent = vazia ? "Preencha a potência e a energia da proposta para comparar."
      : ambos ? "Total de " + comp.map((m) => NOME_SERVICO[m].toLowerCase()).join(" + ") + (comp.length < modos.length ? " (o outro serviço ainda não tem proposta)" : "")
      : (m0.proposta.nome || "Proposta") + " face a " + (m0.atual.nome || "fornecedor atual");
    $("#kpi-ano").textContent = vazia ? "—" : fmtE(ano);
    $("#kpi-pct").textContent = !vazia && totA > 0 ? fmtN((poup / totA) * 100, 1) + "%" : "—";
    $("#kpi-24").textContent = vazia ? "—" : fmtE(ano * 2);
    $("#fixa-val").textContent = vazia ? "—" : (poup < -0.005 ? "−" : "") + fmtE(Math.abs(poup));

    // Barras
    const valA = vazia ? todosA : totA;
    const max = Math.max(valA, totP, 0.01);
    $("#b-a").style.width = (valA / max) * 100 + "%";
    $("#b-p").style.width = (vazia ? 0 : (totP / max) * 100) + "%";
    $("#b-v-a").textContent = fmtE(valA);
    $("#b-v-p").textContent = vazia ? "—" : fmtE(totP);
    const nomeA = ambos ? "Atual" : m0.atual.nome || "Atual";
    const nomeP = ambos ? "Proposta" : m0.proposta.nome || "Proposta";
    $("#b-nome-a").textContent = nomeA;
    $("#b-nome-p").textContent = nomeP;
    $("#th-a").textContent = nomeA;
    $("#th-p").textContent = nomeP;

    // Tabela (uma secção por serviço)
    const sinal = (d) => (d > 0.004 ? "bom" : d < -0.004 ? "mau" : "");
    const resumo = (cls, nome, va, vp) => {
      const d = va - vp;
      return '<tr class="' + cls + '"><td>' + nome + '</td><td class="num">' + fmtE(va) + '</td><td class="num">' + fmtE(vp) + '</td><td class="num ' + sinal(d) + '">' + fmtSinal(d) + "</td></tr>";
    };
    const seccao = (m, a, p) => {
      const rot = ROTULOS[m], sec = " sec-" + m;
      const get = (r, c) => r.linhas.find((l) => l.chave === c);
      const linhaTab = (chave) => {
        const la = get(a, chave), lp = get(p, chave);
        if (!la && !lp) return "";
        const va = la ? la.base : 0, vp = lp ? lp.base : 0, d = va - vp;
        const cel = (v, q) => '<td class="num">' + fmtE(v) + "<small>" + detalhe(chave, q, m) + "</small></td>";
        return '<tr class="' + sec.trim() + '"><td>' + rot[chave] + "</td>" + cel(va, "atual") + cel(vp, "proposta") + '<td class="num ' + sinal(d) + '">' + fmtSinal(d) + "</td></tr>";
      };
      let h = linhaTab("potencia") + linhaTab("energia") + resumo("sub" + sec, "Subtotal antes de impostos", a.subtotal, p.subtotal);
      h += ["iec", "dgeg", "cav"].map(linhaTab).join("") + linhaTab("outros");
      h += resumo(sec.trim(), "IVA (taxas legais por componente)", a.iva, p.iva);
      h += resumo("tot" + sec, ambos ? "Total " + NOME_SERVICO[m].toLowerCase() + " (estimado)" : "Total da fatura (estimado)", a.total, p.total);
      return h;
    };
    let html = "";
    modos.forEach((m) => {
      if (ambos) html += '<tr class="grupo"><td colspan="4">' + NOME_SERVICO[m] + " · " + esc(state[m].atual.nome || "atual") + " → " + esc(state[m].proposta.nome || "proposta") + "</td></tr>";
      html += seccao(m, res[m].a, res[m].p);
    });
    if (ambos) html += resumo("tot", "Total geral (" + (vazia ? "sem proposta" : comp.map((m) => NOME_SERVICO[m].toLowerCase()).join(" + ")) + ")", vazia ? todosA : totA, vazia ? 0 : totP);
    $("#tbody").innerHTML = html;
    modos.filter(propostaVazia).forEach((m) => {
      $$("#tbody tr.sec-" + m).forEach((tr) => {
        tr.children[2].textContent = "—";
        tr.children[3].textContent = "—";
        tr.children[3].className = "num";
      });
    });
    if (ambos && vazia) {
      const ult = $("#tbody tr:last-child");
      ult.children[2].textContent = "—";
      ult.children[3].textContent = "—";
      ult.children[3].className = "num";
    }

    // Cabeçalho de impressão
    $("#p-cliente").textContent = state.meta.cliente || "—";
    $("#p-consultor").textContent = state.meta.consultor || "Consultoria energética";
    $("#p-data").textContent = new Date().toLocaleDateString("pt-PT", { day: "2-digit", month: "long", year: "numeric" });

    gravar();
  }

  /* ---------- Ações ---------- */
  function toast(txt) {
    const t = $("#toast");
    t.textContent = txt;
    t.classList.add("ver");
    clearTimeout(toast.t);
    toast.t = setTimeout(() => t.classList.remove("ver"), 2200);
  }

  $$(".sep").forEach((b) => b.addEventListener("click", () => {
    state.vista = b.dataset.modo;
    preencher();
    atualizar();
  }));

  $("#btn-exemplo").addEventListener("click", () => {
    const def = MODO_DEFAULT();
    servicosDaVista().forEach((m) => { state[m] = def[m]; });
    preencher();
    atualizar();
    toast("Exemplo carregado");
  });

  $("#btn-repor-fiscal").addEventListener("click", () => {
    state.fiscal = { ...FISCAL_DEFAULT };
    preencher();
    atualizar();
    toast("Parâmetros repostos");
  });

  $("#btn-imprimir").addEventListener("click", () => window.print());
  window.addEventListener("beforeprint", () => {
    document.title = §Proposta energética${state.meta.cliente ? " - " + state.meta.cliente : ""}§;
  });

  $("#btn-copiar").addEventListener("click", async () => {
    if (!ultimo) return;
    const linhas = ["Proposta de poupança" + (state.meta.cliente ? " — " + state.meta.cliente : "")];
    let totalPoup = 0, totalAno = 0, algum = false;
    Object.keys(ultimo).forEach((m) => {
      if (propostaVazia(m)) return;
      algum = true;
      const { a, p } = ultimo[m], x = state[m], d = a.total - p.total;
      totalPoup += d;
      totalAno += (d * 365) / diasDe(m);
      linhas.push(NOME_SERVICO[m] + " (" + fmtN(diasDe(m), 0) + " dias): " + (x.atual.nome || "atual") + " " + fmtE(a.total) + " → " + (x.proposta.nome || "proposta") + " " + fmtE(p.total) + " · poupança " + fmtE(d));
    });
    if (!algum) { toast("Preencha a proposta para copiar o resumo"); return; }
    linhas.push("Poupança total: " + fmtE(totalPoup) + " por fatura · " + fmtE(totalAno) + " por ano", "Valores estimativos com IVA incluído.");
    const txt = linhas.join("\n");
    try { await navigator.clipboard.writeText(txt); toast("Resumo copiado"); }
    catch (e) { window.prompt("Copie o resumo:", txt); }
  });

  /* ---------- Leitura de faturas ---------- */
  let leituraDisponivel = false;
  let lidosPorModo = { eletricidade: [], gas: [] };

  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

  async function verificarLeitura() {
    try {
      const r = await fetch("api/estado", { cache: "no-store" });
      if (!r.ok) throw new Error("sem estado");
      const j = await r.json();
      leituraDisponivel = true;
      $("#dropzone").classList.remove("desativada");
      $("#dropzone-tabela").classList.remove("desativada");
      $("#dropzone-tabela-gas").classList.remove("desativada");
      const pill = $("#pill-leitura");
      const local = j.tesseract && j.poppler && j.paginas;
      pill.textContent = j.ia ? "leitura por IA" : local ? "leitura local (OCR)" : j.poppler ? "leitura local (PDFs de texto)" : "leitura básica";
      pill.title = j.ia ? "Modelo: " + j.modelo : local ? "Tesseract e Poppler/Xpdf instalados" : "Instale Tesseract e Poppler (ou Xpdf) para ler fotos e PDFs digitalizados";
      $("#nota-priv").hidden = !j.ia;
      const faltam = [];
      if (!j.tesseract) faltam.push("Tesseract (fotos e PDFs digitalizados)");
      if (!j.poppler) faltam.push("pdftotext (Poppler, Xpdf ou o que vem com o Git, para PDFs)");
      if (j.poppler && !j.paginas) faltam.push("pdftoppm ou pdftopng (para PDFs digitalizados)");
      $("#nota-ocr").hidden = !faltam.length;
      $("#nota-ocr").textContent = "Leitura local: faltam " + faltam.join(" e ") + ". Indique as pastas em «Ferramentas de leitura local» (abaixo).";
      mostrarChave(j);
      mostrarFerramentas();
    } catch (e) {
      desativarLeitura();
    }
  }

  function desativarLeitura() {
    leituraDisponivel = false;
    $("#dropzone").classList.add("desativada");
    $("#dropzone-tabela").classList.add("desativada");
    $("#dropzone-tabela-gas").classList.add("desativada");
    $("#pill-leitura").textContent = "requer servidor Go";
  }

  function mostrarLeitura(tipo, corpo) {
    const cls = { aguarde: "leitura-aguarde", erro: "leitura-erro", ok: "leitura-ok" }[tipo];
    $("#leitura").innerHTML = '<div class="' + cls + '">' + corpo + "</div>";
  }

  function garantirOpcaoKva() {
    const sel = document.querySelector('select[data-k="potenciaKva"]');
    const v = state.eletricidade.perfil.potenciaKva;
    if (!sel || !Number.isFinite(v)) return;
    if (![...sel.options].some((o) => parseFloat(o.value) === v)) {
      const o = document.createElement("option");
      o.textContent = String(v);
      sel.appendChild(o);
    }
  }

  function destacar() {
    $$(".lido").forEach((el) => el.classList.remove("lido"));
    ["eletricidade", "gas"].forEach((modo) => (lidosPorModo[modo] || []).forEach(([s, k]) => {
      const el = document.querySelector('[data-m="' + modo + '"] [data-s="' + s + '"][data-k="' + k + '"]');
      if (el) el.classList.add("lido");
    }));
  }

  function verTexto(j) {
    if (!j.textoLido) return "";
    return '<details><summary>Ver texto lido do ficheiro</summary><pre class="texto-lido">' + esc(j.textoLido) + "</pre></details>";
  }

  function resultadoAtual(modo) {
    const m = state[modo];
    return calcularMotor({ tipo: modo, ...m.perfil, ...m.atual, fiscal: { ...state.fiscal } });
  }

  // Diferença para o total da fatura, com a CAV incluída ou, se a fatura a cobra à parte, sem ela.
  function diferencaTotal(modo, tot) {
    const r = resultadoAtual(modo);
    const cav = r.linhas.find((l) => l.chave === "cav");
    const cavTot = cav ? cav.total : 0;
    return Math.min(Math.abs(r.total - tot), Math.abs(r.total - cavTot - tot));
  }

  function totalCalculado(modo) {
    const m = state[modo];
    return calcularMotor({ tipo: modo, ...m.perfil, ...m.atual, fiscal: { ...state.fiscal } }).total;
  }

  // Testa as variantes (CAV na fatura ou fora; desconto também na potência) e fica com a que
  // reproduz o total real da fatura.
  function calibrar(modo, totalFatura) {
    const m = state[modo], f = state.fiscal;
    const padrao = { cav: f.cavAplica, dp: !!m.atual.descontoPotencia };
    const cavs = [padrao.cav]; // a CAV fica sempre incluída
    const dps = modo === "eletricidade" && m.atual.descontoPct > 0 ? [padrao.dp, !padrao.dp] : [padrao.dp];
    let melhor = null;
    cavs.forEach((cav) => dps.forEach((dp) => {
      f.cavAplica = cav;
      m.atual.descontoPotencia = dp;
      const d = diferencaTotal(modo, totalFatura);
      if (!melhor || d < melhor.d - 0.005) melhor = { cav, dp, d };
    }));
    f.cavAplica = padrao.cav;
    m.atual.descontoPotencia = padrao.dp;
    if (melhor.d >= 0.1) return [];
    f.cavAplica = melhor.cav;
    m.atual.descontoPotencia = melhor.dp;
    const notas = [];
    if (melhor.dp !== padrao.dp) notas.push(melhor.dp ? "desconto aplicado também à potência" : "desconto só na energia");
    if (melhor.cav !== padrao.cav) notas.push(melhor.cav ? "CAV incluída" : "CAV fora desta fatura (cobrada à parte)");
    return notas;
  }

  function aplicarExtracao(j) {
    const lidos = [], faltam = [], modos = [], totais = [];
    lidosPorModo = { eletricidade: [], gas: [] };
    const num = (v) => (typeof v === "number" && Number.isFinite(v) ? v : null);
    const poe = (modo, secao, chave, valor, rotulo, essencial) => {
      const n = num(valor);
      if (n === null) { if (essencial) faltam.push(rotulo); return; }
      state[modo][secao][chave] = chave === "dias" ? Math.round(n) : n;
      lidosPorModo[modo].push([secao, chave]);
      lidos.push(rotulo + ": " + fmtN(n, 4));
    };
    const zero = (modo, chave, valor) => { const n = num(valor); state[modo].atual[chave] = n === null ? 0 : n; };

    const nome = String(j.comercializador || "").trim();
    if (j.periodo) lidos.push("Período: " + String(j.periodo).trim());
    if (j.cliente && !state.meta.cliente) state.meta.cliente = String(j.cliente).trim();
    const e = j.eletricidade, g = j.gas;

    if (e) {
      modos.push("eletricidade");
      const m = state.eletricidade;
      if (nome) m.atual.nome = nome;
      poe("eletricidade", "perfil", "potenciaKva", e.potenciaKva, "Potência (kVA)", true);
      poe("eletricidade", "perfil", "consumoKwh", e.consumoKwh, "Consumo (kWh)", true);
      poe("eletricidade", "perfil", "dias", e.dias, "Dias", true);
      const bi = e.tarifa === "bi";
      m.perfil.tarifa = bi ? "bi" : "simples";
      if (bi) {
        poe("eletricidade", "perfil", "vazioPct", e.vazioPct, "Consumo em vazio (%)", false);
        poe("eletricidade", "atual", "precoVazio", e.precoVazio, "Preço vazio (€/kWh)", true);
        poe("eletricidade", "atual", "precoForaVazio", e.precoForaVazio, "Preço fora do vazio (€/kWh)", true);
      } else {
        poe("eletricidade", "atual", "precoSimples", e.precoSimples, "Preço energia (€/kWh)", true);
      }
      poe("eletricidade", "atual", "potDia", e.potDia, "Potência (€/dia)", true);
      zero("eletricidade", "descontoPct", e.descontoPct);
      zero("eletricidade", "outrosMes", e.outrosMes);
      if (num(e.totalFatura) !== null) totais.push(["Eletricidade", "eletricidade", e.totalFatura]);
    }
    if (g) {
      modos.push("gas");
      const m = state.gas;
      if (nome) m.atual.nome = nome;
      poe("gas", "perfil", "consumoKwh", g.consumoKwh, "Gás · consumo (kWh)", true);
      poe("gas", "perfil", "dias", g.dias, "Gás · dias", true);
      poe("gas", "perfil", "escalao", g.escalao, "Gás · escalão", false);
      poe("gas", "atual", "potDia", g.termoFixoDia, "Gás · termo fixo (€/dia)", true);
      poe("gas", "atual", "precoSimples", g.precoKwh, "Gás · preço (€/kWh)", true);
      zero("gas", "descontoPct", g.descontoPct);
      zero("gas", "outrosMes", g.outrosMes);
      if (num(g.totalFatura) !== null) totais.push(["Gás natural", "gas", g.totalFatura]);
    }

    if (!modos.length) {
      mostrarLeitura("erro", "Não encontrei dados de eletricidade ou gás nesta fatura. " + esc(j.observacoes || "") + " Preencha os dados manualmente." + verTexto(j));
      return;
    }
    const calib = [];
    totais.forEach(([rot, modo, tot]) => {
      const notas = calibrar(modo, tot);
      if (notas.length) calib.push(rot + ": " + notas.join(", "));
    });
    state.vista = modos.length > 1 ? "ambos" : modos[0];
    aplicarTabela();
    aplicarTabelaGas();
    preencher();
    atualizar();

    let h = "<b>Fatura lida" + (nome ? " · " + esc(nome) : "") + "</b> ";
    h += '<span class="chip">' + ({ ia: "leitura por IA", ocr: "leitura local (OCR)" }[j.metodo] || "leitura local") + "</span>";
    h += '<div class="chips">' + lidos.map((t) => '<span class="chip">' + esc(t) + "</span>").join("") + "</div>";
    if (faltam.length) h += "<p><b>Não encontrado:</b> " + esc(faltam.join(", ")) + ". Preencha manualmente.</p>";
    totais.forEach(([rot, modo, tot]) => {
      const r = resultadoAtual(modo);
      const cav = r.linhas.find((l) => l.chave === "cav");
      const cavTot = cav ? cav.total : 0;
      const comCav = Math.abs(r.total - tot);
      const semCav = Math.abs(r.total - cavTot - tot);
      let aviso;
      if (comCav < 0.5) aviso = " (confere)";
      else if (cavTot > 0 && semCav < 0.5) aviso = " (confere sem a CAV de " + fmtE(cavTot) + ", que esta fatura cobra à parte; aqui ela está incluída)";
      else aviso = " (diferença de " + fmtE(Math.min(comCav, semCav)) + ": confira preços e taxas)";
      h += "<p>" + rot + ": total na fatura <b>" + fmtE(tot) + "</b> · calculado aqui <b>" + fmtE(r.total) + "</b>" + aviso + "</p>";
    });
    if (calib.length) h += "<p>Ajustado para reproduzir o total real da fatura: " + esc(calib.join("; ")) + ".</p>";
    if (j.observacoes) h += "<p>" + esc(j.observacoes) + "</p>";
    if (modos.length > 1) h += "<p>Esta fatura tem eletricidade e gás: os dois serviços aparecem juntos na aba «Eletricidade + Gás».</p>";
    h += "<p>Confirme os campos destacados a verde e preencha a proposta para comparar.</p>";
    h += verTexto(j);
    mostrarLeitura("ok", h);
  }

  async function lerFatura(ficheiro) {
    if (!ficheiro) return;
    if (!leituraDisponivel) {
      mostrarLeitura("erro", "A leitura automática precisa do servidor Go: execute <b>go run main.go</b> e abra <b>http://localhost:8080</b>. Pode continuar a preencher manualmente.");
      return;
    }
    if (ficheiro.size > 20 * 1024 * 1024) {
      mostrarLeitura("erro", "Ficheiro demasiado grande (máximo 20 MB).");
      return;
    }
    mostrarLeitura("aguarde", "A ler <b>" + esc(ficheiro.name || "fatura") + "</b>…");
    try {
      const fd = new FormData();
      fd.append("ficheiro", ficheiro, ficheiro.name || "fatura");
      const r = await fetch("api/extrair", { method: "POST", body: fd });
      const j = await r.json().catch(() => ({}));
      if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
      aplicarExtracao(j);
    } catch (e) {
      mostrarLeitura("erro", esc(e.message) + "<br>Pode preencher os dados manualmente.");
    }
  }

  const dz = $("#dropzone"), inputFatura = $("#ficheiro-fatura");
  inputFatura.addEventListener("change", () => { lerFatura(inputFatura.files[0]); inputFatura.value = ""; });
  ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("sobre"); }));
  ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("sobre"); }));
  dz.addEventListener("drop", (e) => lerFatura(e.dataTransfer.files[0]));
  window.addEventListener("dragover", (e) => e.preventDefault());
  window.addEventListener("drop", (e) => e.preventDefault());
  document.addEventListener("paste", (e) => {
    const f = e.clipboardData && e.clipboardData.files && e.clipboardData.files[0];
    if (f) { e.preventDefault(); lerFatura(f); }
  });


  function mostrarChave(j) {
    $("#chave-ia").hidden = false;
    $("#chave-estado").textContent = j.ia
      ? "· ativa (…" + j.fim + (j.origem === "ambiente" ? ", variável de ambiente)" : ", guardada neste computador)")
      : "· não definida (opcional)";
    $("#btn-remover-chave").hidden = !(j.ia && j.origem === "ficheiro");
    $("#chave-nota").textContent = j.origem === "ambiente"
      ? "Definida pela variável de ambiente ANTHROPIC_API_KEY, que tem prioridade sobre a chave guardada aqui."
      : "A chave fica guardada neste computador (pasta de configuração do utilizador) e não volta a ser mostrada.";
  }

  async function enviarChave(metodo, corpo) {
    const r = await fetch("api/chave", {
      method: metodo,
      headers: { "Content-Type": "application/json" },
      body: corpo ? JSON.stringify(corpo) : undefined,
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
    await verificarLeitura();
  }

  $("#btn-guardar-chave").addEventListener("click", async () => {
    const campo = $("#chave-input");
    try { await enviarChave("POST", { chave: campo.value.trim() }); campo.value = ""; toast("Chave guardada"); }
    catch (e) { toast(e.message); }
  });
  $("#btn-remover-chave").addEventListener("click", async () => {
    try { await enviarChave("DELETE"); toast("Chave removida"); } catch (e) { toast(e.message); }
  });

  async function mostrarFerramentas() {
    try {
      const r = await fetch("api/ferramentas", { cache: "no-store" });
      if (!r.ok) throw new Error("indisponível");
      const j = await r.json();
      $("#ferramentas-ia").hidden = false;
      const f = j.encontradas || {};
      const linha = (nome, caminho) => nome + ": " + (caminho ? "✔ " + caminho : "✘ não encontrado");
      $("#ferr-estado").textContent = (f.pdftotext ? "· PDFs ✔" : "· PDFs ✘") + (f.tesseract ? " · OCR ✔" : " · OCR ✘");
      $("#ferr-nota").textContent = [linha("pdftotext", f.pdftotext), linha("pdftoppm/pdftopng", f.pdftoppm || f.pdftopng), linha("tesseract", f.tesseract)].join("  |  ");
      if (document.activeElement !== $("#ferr-poppler")) $("#ferr-poppler").value = j.poppler || "";
      if (document.activeElement !== $("#ferr-tesseract")) $("#ferr-tesseract").value = j.tesseract || "";
    } catch (e) {
      $("#ferramentas-ia").hidden = true;
    }
  }

  $("#btn-guardar-ferr").addEventListener("click", async () => {
    try {
      const r = await fetch("api/ferramentas", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ poppler: $("#ferr-poppler").value.trim(), tesseract: $("#ferr-tesseract").value.trim() }),
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
      toast("Caminhos guardados");
      await verificarLeitura();
    } catch (e) {
      toast(e.message);
    }
  });

/* ---------- Confirmação Sim / Não ---------- */
  function confirmarSimNao(mensagem) {
    return new Promise((resolve) => {
      const fundo = document.createElement("div");
      fundo.className = "modal-fundo";
      fundo.innerHTML = '<div class="modal" role="alertdialog" aria-modal="true" aria-labelledby="modal-msg"><p id="modal-msg"></p>' +
        '<div class="modal-acoes"><button type="button" class="btn btn-primario" data-r="sim">Sim</button><button type="button" class="btn" data-r="nao">Não</button></div></div>';
      fundo.querySelector("#modal-msg").textContent = mensagem;
      const anterior = document.activeElement;
      const fechar = (resposta) => {
        document.removeEventListener("keydown", tecla);
        fundo.remove();
        if (anterior && anterior.focus) anterior.focus();
        resolve(resposta);
      };
      const tecla = (e) => { if (e.key === "Escape") { e.preventDefault(); fechar(false); } };
      fundo.addEventListener("click", (e) => {
        const b = e.target.closest("[data-r]");
        if (b) fechar(b.dataset.r === "sim");
        else if (e.target === fundo) fechar(false);
      });
      document.addEventListener("keydown", tecla);
      document.body.appendChild(fundo);
      fundo.querySelector('[data-r="nao"]').focus();
    });
  }

  /* ---------- Limpar dados ---------- */
  function estadoVazio() {
    const fornecedor = (extra) => ({ nome: "", potDia: null, precoSimples: null, descontoPct: null, descontoPotencia: false, outrosMes: null, ...extra });
    const bi = { precoVazio: null, precoForaVazio: null };
    return {
      eletricidade: {
        perfil: { potenciaKva: null, consumoKwh: null, dias: null, tarifa: "simples", vazioPct: null },
        atual: fornecedor(bi),
        proposta: fornecedor(bi),
      },
      gas: {
        perfil: { consumoKwh: null, dias: null, tarifa: "simples", escalao: null },
        atual: fornecedor({}),
        proposta: fornecedor({}),
      },
    };
  }

  $("#btn-limpar").addEventListener("click", async () => {
    if (!(await confirmarSimNao("Deseja mesmo limpar dados?"))) return;
    const v = estadoVazio();
    state.eletricidade = v.eletricidade;
    state.gas = v.gas;
    state.meta.cliente = "";
    lidosPorModo = { eletricidade: [], gas: [] };
    $("#leitura").innerHTML = "";
    preencher();
    atualizar();
    estadoTabela();
    estadoTabelaGas();
    toast("Dados limpos");
  });

  /* ---------- Tabela de preços da proposta (outra concessionária) ---------- */
  function prepararBlocos() {
    const sufixo = { eletricidade: "Eletricidade", gas: "Gás natural" };
    ["#bloco-perfil", "#bloco-fornecedores"].forEach((sel) => {
      const el = $(sel);
      const copia = el.cloneNode(true);
      copia.removeAttribute("id");
      copia.querySelectorAll("[id]").forEach((x) => x.removeAttribute("id"));
      copia.querySelectorAll(".bloco-cab .btn-link").forEach((b) => b.remove());
      el.dataset.m = "eletricidade";
      el.dataset.vista = "eletricidade";
      copia.dataset.m = "gas";
      copia.dataset.vista = "gas";
      $("#bloco-tabela-gas").before(copia);
      [el, copia].forEach((bl) => bl.querySelectorAll("h2").forEach((h) => { h.textContent += " · " + sufixo[bl.dataset.m]; }));
    });
  }
  prepararBlocos();

  const POTENCIAS = [1.15, 2.3, 3.45, 4.6, 5.75, 6.9, 10.35, 13.8, 17.25, 20.7, 27.6, 34.5, 41.4];
  const NOMES_COMERCIALIZADORES = ["EDP Comercial", "Endesa", "Iberdrola", "Galp", "Goldenergy", "Gold Energy", "SU Eletricidade", "Repsol", "Luzboa", "Plenitude", "Ibelectra", "MEO Energia", "Coopérnico", "Elergone", "Axpo", "Nossa Energia", "Ylce", "Audax", "Fortia"];
  const numPT = (s) => parseFloat(String(s).replace(",", "."));
  const finito = (x) => typeof x === "number" && Number.isFinite(x);

  // BEGIN-PARSER
  function normalizarTexto(t) {
    return String(t)
      .replace(/\u00a0/g, " ")
      .replace(/[;|\t]+/g, "  ")
      .replace(/(\d+[.,]\d{1,6})\d{4,}/g, "$1")
      .replace(/(\d+[.,]\d*?)0+(?=\D|$)/g, "$1")
      .replace(/(\d)[.,](?=\D|$)/g, "$1");
  }

  // Colunas reconhecidas numa linha de cabeçalho, pela ordem em que aparecem.
  function colunasDe(linha) {
    const l = linha.toLowerCase();
    const cols = [];
    const add = (chave, re) => { const m = re.exec(l); if (m) cols.push({ chave, pos: m.index }); };
    add("potDia", /termo|\/\s*dia|di[áa]ri/);
    add("simples", /simples/);
    add("fora", /fora\s*(?:de\s*)?vazio|cheias?/);
    const re = /vazio/g;
    let m;
    while ((m = re.exec(l))) {
      const antes = l.slice(Math.max(0, m.index - 12), m.index);
      if (!/fora\s*(?:de\s*)?$/.test(antes) && !/super\s*$/.test(antes)) { cols.push({ chave: "vazio", pos: m.index }); break; }
    }
    return cols.sort((a, b) => a.pos - b.pos).map((c) => c.chave);
  }

  function analisarTabela(texto) {
    const limpo = normalizarTexto(texto);
    const linhas = limpo.split(/\r?\n/);
    const res = { nome: "", linhas: [], global: { simples: null, vazio: null, fora: null, descontoPct: null }, avisos: [] };
    const baixo = limpo.toLowerCase();
    const achados = NOMES_COMERCIALIZADORES.map((n) => ({ n, i: baixo.indexOf(n.toLowerCase()) })).filter((x) => x.i >= 0).sort((a, b) => a.i - b.i);
    if (achados.length) res.nome = achados[0].n;

    // linhas de dados: começam pela potência (kVA) seguida de preços
    const reLinha = /^\s*(?:pot[êe]ncia\s*)?(\d{1,2}(?:[.,]\d{1,2})?)\s*(?:kva)?(?=\s|$)(.*)$/i;
    const dados = [];
    linhas.forEach((ln, i) => {
      const m = ln.match(reLinha);
      if (!m) return;
      const canon = POTENCIAS.find((p) => Math.abs(p - numPT(m[1])) < 0.005);
      if (canon === undefined) return;
      const precos = (m[2].match(/\d+[.,]\d{1,6}/g) || []).map(numPT).filter((v) => v > 0 && v < 5);
      if (precos.length) dados.push({ i, kva: canon, precos });
    });
    const indicesDados = new Set(dados.map((d) => d.i));

    const cabecalho = (i) => {
      for (let j = i - 1; j >= Math.max(0, i - 14); j--) {
        if (indicesDados.has(j)) break;
        const c = colunasDe(linhas[j]);
        if (c.length) return c;
      }
      return null;
    };
    const tipoNoContexto = (i) => {
      for (let j = i - 1; j >= Math.max(0, i - 14); j--) {
        if (indicesDados.has(j)) break;
        if (/(?:bi|tri)-?\s*hor[áa]rio/i.test(linhas[j])) return "bi";
        if (/simples/i.test(linhas[j])) return "simples";
      }
      return "";
    };

    const porKva = new Map();
    let ultimo = null;
    dados.forEach((d) => {
      let cols = cabecalho(d.i);
      if (cols) {
        if (!cols.some((c) => c !== "potDia")) {
          const tipo = tipoNoContexto(d.i);
          cols = cols.concat(tipo === "bi" || (!tipo && d.precos.length >= 3) ? ["vazio", "fora"] : ["simples"]);
        }
        if (cols[0] !== "potDia") cols = ["potDia"].concat(cols);
        ultimo = cols;
      } else if (ultimo) {
        cols = ultimo;
      } else {
        cols = d.precos.length >= 3 ? ["potDia", "vazio", "fora"] : ["potDia", "simples"];
      }
      let reg = porKva.get(d.kva);
      if (!reg) { reg = { kva: d.kva, potDia: null, simples: null, vazio: null, fora: null }; porKva.set(d.kva, reg); }
      cols.forEach((c, k) => { if (k < d.precos.length && reg[c] === null) reg[c] = d.precos[k]; });
    });
    res.linhas = [...porKva.values()].sort((a, b) => a.kva - b.kva);

    // preços de energia comuns a todas as potências ("Energia simples 0,1450 €/kWh")
    linhas.forEach((ln, i) => {
      if (indicesDados.has(i)) return;
      const l = ln.toLowerCase();
      if (/kva/.test(l) || !/kwh|energia/.test(l)) return;
      const pr = (ln.match(/\d+[.,]\d{1,6}/g) || []).map(numPT).filter((v) => v > 0 && v < 1);
      if (!pr.length) return;
      if (/fora\s*(?:de\s*)?vazio|cheia/.test(l)) { if (res.global.fora === null) res.global.fora = pr[0]; }
      else if (/vazio/.test(l)) { if (res.global.vazio === null) res.global.vazio = pr[0]; }
      else if (/simples/.test(l)) { if (res.global.simples === null) res.global.simples = pr[0]; }
    });
    const md = limpo.match(/desconto[^0-9\n]{0,30}(\d{1,2}(?:[.,]\d{1,2})?)\s*%/i) || limpo.match(/(\d{1,2}(?:[.,]\d{1,2})?)\s*%\s*desconto/i);
    if (md) res.global.descontoPct = numPT(md[1]);
    if (!res.linhas.length) res.avisos.push("Não encontrei linhas por potência (kVA).");
    return res;
  }

  // Tabela de gás natural: linhas por escalão com termo fixo (€/dia) e energia (€/kWh).
  function analisarTabelaGas(texto) {
    const limpo = normalizarTexto(texto);
    const linhas = limpo.split(/\r?\n/);
    const res = { nome: "", linhas: [], global: { termo: null, preco: null, descontoPct: null }, avisos: [] };
    const baixo = limpo.toLowerCase();
    const achados = NOMES_COMERCIALIZADORES.map((n) => ({ n, i: baixo.indexOf(n.toLowerCase()) })).filter((x) => x.i >= 0).sort((a, b) => a.i - b.i);
    if (achados.length) res.nome = achados[0].n;
    const precosDe = (t) => (t.match(/\d+[.,]\d{1,6}/g) || []).map(numPT).filter((v) => v > 0 && v < 5);
    const colunas = (linha) => {
      const l = linha.toLowerCase(), cols = [];
      const add = (chave, re) => { const m = re.exec(l); if (m) cols.push({ chave, pos: m.index }); };
      add("termo", /termo|\/\s*dia|di[áa]ri/);
      add("preco", /energia|kwh|pre[çc]o/);
      return cols.sort((a, b) => a.pos - b.pos).map((c) => c.chave);
    };
    const dados = [];
    linhas.forEach((ln, i) => {
      let m = ln.match(/escal[ãa]o\s*(\d)(.*)$/i);
      if (!m) {
        // "1  0,18  0,085" quando há um cabeçalho com "escalão" logo acima
        const c = ln.match(/^\s*([1-4])\s+(.*)$/);
        const cab = linhas.slice(Math.max(0, i - 8), i).some((x) => /escal[ãa]o/i.test(x));
        if (!c || !cab) return;
        m = c;
      }
      const precos = precosDe(m[2]);
      if (precos.length) dados.push({ i, escalao: Number(m[1]), precos });
    });
    const indices = new Set(dados.map((d) => d.i));
    const porEscalao = new Map();
    let ultimo = null;
    dados.forEach((d) => {
      let cols = null;
      for (let j = d.i - 1; j >= Math.max(0, d.i - 14) && !cols; j--) {
        if (indices.has(j)) break;
        const c = colunas(linhas[j]);
        if (c.length) cols = c;
      }
      if (cols) {
        if (cols.length === 1) cols = cols[0] === "termo" ? ["termo", "preco"] : ["termo", "preco"];
        ultimo = cols;
      } else cols = ultimo || ["termo", "preco"];
      let reg = porEscalao.get(d.escalao);
      if (!reg) { reg = { escalao: d.escalao, termo: null, preco: null }; porEscalao.set(d.escalao, reg); }
      cols.forEach((c, k) => { if (k < d.precos.length && reg[c] === null) reg[c] = d.precos[k]; });
    });
    res.linhas = [...porEscalao.values()].sort((a, b) => a.escalao - b.escalao);
    // valores comuns a todos os escalões
    linhas.forEach((ln, i) => {
      if (indices.has(i) || /escal[ãa]o/i.test(ln)) return;
      const l = ln.toLowerCase();
      if (/termo\s*fixo/.test(l)) {
        const pr = precosDe(ln);
        if (pr.length && res.global.termo === null) res.global.termo = pr[0];
      } else if (/kwh|energia/.test(l)) {
        const pr = precosDe(ln).filter((v) => v < 1);
        if (pr.length && res.global.preco === null) res.global.preco = pr[0];
      }
    });
    const md = limpo.match(/desconto[^0-9\n]{0,30}(\d{1,2}(?:[.,]\d{1,2})?)\s*%/i) || limpo.match(/(\d{1,2}(?:[.,]\d{1,2})?)\s*%\s*desconto/i);
    if (md) res.global.descontoPct = numPT(md[1]);
    if (!res.linhas.length && !(res.global.termo !== null || res.global.preco !== null)) res.avisos.push("Não encontrei preços por escalão.");
    return res;
  }
  // END-PARSER

  const temGlobal = (g) => finito(g.simples) || finito(g.vazio) || finito(g.fora);

  function linhaDaTabela() {
    const kva = state.eletricidade.perfil.potenciaKva;
    if (!finito(kva)) return null;
    return (state.tabela.linhas || []).find((l) => finito(l.kva) && Math.abs(l.kva - kva) < 0.005) || null;
  }

  // Preenche a proposta com a linha da tabela que corresponde à potência e ao tarifário do cliente.
  function aplicarTabela() {
    estadoTabela();
    const t = state.tabela, m = state.eletricidade, uso = $("#tabela-uso");
    if (!t.linhas.length && !temGlobal(t.global)) { uso.textContent = ""; return; }
    const kva = m.perfil.potenciaKva;
    if (!finito(kva)) { uso.textContent = "Escolha a potência contratada do cliente para aplicar a tabela."; return; }
    const l = linhaDaTabela();
    if (!l) {
      uso.textContent = "A tabela não tem a potência de " + fmtN(kva, 2) + " kVA: preencha a proposta à mão ou adicione essa linha.";
      return;
    }
    const g = t.global || {};
    const val = (a, b) => (finito(a) && a > 0 ? a : finito(b) && b > 0 ? b : null);
    const p = m.proposta, partes = [];
    if (t.nome) p.nome = t.nome;
    p.potDia = val(l.potDia, null);
    if (p.potDia !== null) partes.push("potência " + fmtN(p.potDia, 4) + " €/dia");
    if (m.perfil.tarifa === "bi") {
      p.precoVazio = val(l.vazio, g.vazio);
      p.precoForaVazio = val(l.fora, g.fora);
      if (p.precoVazio !== null) partes.push("vazio " + fmtN(p.precoVazio, 4) + " €/kWh");
      if (p.precoForaVazio !== null) partes.push("fora de vazio " + fmtN(p.precoForaVazio, 4) + " €/kWh");
    } else {
      p.precoSimples = val(l.simples, g.simples);
      if (p.precoSimples !== null) partes.push("energia " + fmtN(p.precoSimples, 4) + " €/kWh");
    }
    if (finito(g.descontoPct)) { p.descontoPct = g.descontoPct; partes.push("desconto " + fmtN(g.descontoPct, 2) + "%"); }
    uso.textContent = "Linha usada para " + fmtN(kva, 2) + " kVA (" + (m.perfil.tarifa === "bi" ? "bi-horário" : "simples") + "): " + partes.join(" · ") + ".";
  }

  function estadoTabela() {
    const n = state.tabela.linhas.length;
    $("#tabela-estado").textContent = n ? n + (n === 1 ? " potência" : " potências") : temGlobal(state.tabela.global) ? "só preços de energia" : "sem tabela";
    const ativa = (state.tabela.linhas || []).indexOf(linhaDaTabela());
    $$("#tabela-corpo tr").forEach((tr, i) => tr.classList.toggle("ativa", i === ativa));
  }

  function celulaTab(i, k, v, rot) {
    return '<td><input type="number" inputmode="decimal" step="any" min="0" data-ti="' + i + '" data-tk="' + k + '" aria-label="' + rot + '" value="' + (finito(v) ? v : "") + '"></td>';
  }

  function renderTabela() {
    $("#tabela-corpo").innerHTML = state.tabela.linhas.map((l, i) =>
      "<tr>" + celulaTab(i, "kva", l.kva, "kVA") + celulaTab(i, "potDia", l.potDia, "Potência em euros por dia") +
      celulaTab(i, "simples", l.simples, "Energia simples") + celulaTab(i, "vazio", l.vazio, "Vazio") + celulaTab(i, "fora", l.fora, "Fora de vazio") +
      '<td><button type="button" class="rm" data-rm="' + i + '" aria-label="Remover linha">×</button></td></tr>').join("");
    const g = state.tabela.global;
    $("#tg-nome").value = state.tabela.nome || "";
    $("#tg-simples").value = finito(g.simples) ? g.simples : "";
    $("#tg-vazio").value = finito(g.vazio) ? g.vazio : "";
    $("#tg-fora").value = finito(g.fora) ? g.fora : "";
    $("#tg-desc").value = finito(g.descontoPct) ? g.descontoPct : "";
    estadoTabela();
  }

  function reaplicarTabela() {
    aplicarTabela();
    preencher();
    atualizar();
    estadoTabela();
  }

  $("#tabela-corpo").addEventListener("input", (e) => {
    const el = e.target;
    if (el.dataset.ti === undefined) return;
    const n = parseFloat(String(el.value).replace(",", "."));
    state.tabela.linhas[Number(el.dataset.ti)][el.dataset.tk] = Number.isFinite(n) ? n : null;
    reaplicarTabela();
  });
  $("#tabela-corpo").addEventListener("click", (e) => {
    const b = e.target.closest("[data-rm]");
    if (!b) return;
    state.tabela.linhas.splice(Number(b.dataset.rm), 1);
    renderTabela();
    reaplicarTabela();
  });
  $("#btn-add-linha").addEventListener("click", () => {
    state.tabela.linhas.push({ kva: null, potDia: null, simples: null, vazio: null, fora: null });
    renderTabela();
    const campos = $$("#tabela-corpo tr:last-child input");
    if (campos[0]) campos[0].focus();
  });
  $("#btn-limpar-tabela").addEventListener("click", () => {
    state.tabela = TABELA_DEFAULT();
    renderTabela();
    $("#leitura-tabela").innerHTML = "";
    $("#tabela-uso").textContent = "";
    atualizar();
    toast("Tabela limpa");
  });
  $$("[data-tg]").forEach((el) => el.addEventListener("input", () => {
    const n = parseFloat(String(el.value).replace(",", "."));
    state.tabela.global[el.dataset.tg] = Number.isFinite(n) ? n : null;
    reaplicarTabela();
  }));
  $("#tg-nome").addEventListener("input", (e) => {
    state.tabela.nome = e.target.value;
    reaplicarTabela();
  });

  // Fatura de OUTRA concessionária usada como proposta: aplica os preços dela ao consumo do cliente.
  async function propostaDeFatura(ficheiro, modo, msg) {
    const fd = new FormData();
    fd.append("ficheiro", ficheiro, ficheiro.name || "fatura");
    const r = await fetch("api/extrair", { method: "POST", body: fd });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
    const x = modo === "gas" ? j.gas : j.eletricidade;
    if (!x) return false;
    const m = state[modo], p = m.proposta, partes = [], avisos = [];
    const nome = String(j.comercializador || "").trim();
    if (nome) p.nome = nome;
    const pot = modo === "gas" ? x.termoFixoDia : x.potDia;
    p.potDia = finito(pot) ? pot : null;
    if (p.potDia !== null) partes.push((modo === "gas" ? "termo fixo " : "potência ") + fmtN(p.potDia, 4) + " €/dia");
    if (modo === "gas") {
      p.precoSimples = finito(x.precoKwh) ? x.precoKwh : null;
      if (p.precoSimples !== null) partes.push("energia " + fmtN(p.precoSimples, 4) + " €/kWh");
    } else if (m.perfil.tarifa === "bi") {
      p.precoVazio = finito(x.precoVazio) ? x.precoVazio : null;
      p.precoForaVazio = finito(x.precoForaVazio) ? x.precoForaVazio : null;
      if (p.precoVazio !== null) partes.push("vazio " + fmtN(p.precoVazio, 4) + " €/kWh");
      if (p.precoForaVazio !== null) partes.push("fora de vazio " + fmtN(p.precoForaVazio, 4) + " €/kWh");
      if (p.precoVazio === null || p.precoForaVazio === null) avisos.push("Esta fatura não tem preços bi-horários: complete vazio e fora de vazio à mão.");
    } else {
      p.precoSimples = finito(x.precoSimples) ? x.precoSimples : null;
      if (p.precoSimples !== null) partes.push("energia " + fmtN(p.precoSimples, 4) + " €/kWh");
      else avisos.push("Esta fatura não tem preço de tarifa simples: preencha a energia à mão.");
    }
    p.descontoPct = finito(x.descontoPct) && x.descontoPct > 0 ? x.descontoPct : null;
    if (p.descontoPct !== null) partes.push("desconto " + fmtN(p.descontoPct, 2) + "%");
    if (modo === "eletricidade" && finito(x.potenciaKva) && finito(m.perfil.potenciaKva) && Math.abs(x.potenciaKva - m.perfil.potenciaKva) > 0.005)
      avisos.push("A fatura é de " + fmtN(x.potenciaKva, 2) + " kVA e o cliente tem " + fmtN(m.perfil.potenciaKva, 2) + " kVA: confirme o preço da potência.");
    preencher();
    atualizar();
    let h = "<b>Fatura de outra concessionária lida" + (nome ? " · " + esc(nome) : "") + "</b>";
    h += "<p>Preços aplicados ao consumo e à potência do cliente: " + esc(partes.join(" · ") || "nenhum preço encontrado") + ".</p>";
    avisos.forEach((a) => { h += "<p>" + esc(a) + "</p>"; });
    msg("leitura-ok", h);
    return true;
  }

  // Leitura do ficheiro com a tabela da outra concessionária
  async function lerTabela(ficheiro) {
    if (!ficheiro) return;
    const alvo = $("#leitura-tabela");
    const msg = (cls, html) => { alvo.innerHTML = '<div class="' + cls + '">' + html + "</div>"; };
    if (!leituraDisponivel) {
      msg("leitura-erro", "A leitura de ficheiros precisa do servidor Go (<b>go run main.go</b>). Pode preencher a tabela à mão, aqui em baixo.");
      return;
    }
    if (ficheiro.size > 20 * 1024 * 1024) { msg("leitura-erro", "Ficheiro demasiado grande (máximo 20 MB)."); return; }
    msg("leitura-aguarde", "A ler <b>" + esc(ficheiro.name || "tabela") + "</b>…");
    try {
      const fd = new FormData();
      fd.append("ficheiro", ficheiro, ficheiro.name || "tabela");
      const r = await fetch("api/ler-texto", { method: "POST", body: fd });
      const j = await r.json().catch(() => ({}));
      if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
      const t = analisarTabela(j.texto);
      const detalhe = '<details><summary>Ver texto lido do ficheiro</summary><pre class="texto-lido">' + esc(String(j.texto).slice(0, 8000)) + "</pre></details>";
      if (!t.linhas.length && !temGlobal(t.global)) {
        if (await propostaDeFatura(ficheiro, "eletricidade", msg)) return;
        msg("leitura-erro", "Não encontrei preços por potência nem uma fatura de eletricidade neste ficheiro. Preencha a tabela à mão ou confira o texto lido." + detalhe);
        return;
      }
      state.tabela = { nome: t.nome, linhas: t.linhas, global: t.global };
      renderTabela();
      reaplicarTabela();
      const kvas = t.linhas.map((l) => fmtN(l.kva, 2)).join(", ");
      let h = "<b>Tabela lida" + (t.nome ? " · " + esc(t.nome) : "") + "</b> ";
      h += '<span class="chip">' + ({ ocr: "leitura local (OCR)" }[j.metodo] || "leitura local") + "</span>";
      h += "<p>" + t.linhas.length + " potência(s): " + esc(kvas || "—") + ". Confirme os valores na tabela abaixo.</p>";
      if (t.avisos.length) h += "<p>" + esc(t.avisos.join(" ")) + "</p>";
      mostrarAvisoLinha(h + detalhe);
    } catch (e) {
      msg("leitura-erro", esc(e.message) + "<br>Pode preencher a tabela à mão.");
    }
  }
  function mostrarAvisoLinha(h) { $("#leitura-tabela").innerHTML = '<div class="leitura-ok">' + h + "</div>"; }

  const dzT = $("#dropzone-tabela"), inputTabela = $("#ficheiro-tabela");
  inputTabela.addEventListener("change", () => { lerTabela(inputTabela.files[0]); inputTabela.value = ""; });
  ["dragenter", "dragover"].forEach((ev) => dzT.addEventListener(ev, (e) => { e.preventDefault(); dzT.classList.add("sobre"); }));
  ["dragleave", "drop"].forEach((ev) => dzT.addEventListener(ev, (e) => { e.preventDefault(); dzT.classList.remove("sobre"); }));
  dzT.addEventListener("drop", (e) => lerTabela(e.dataTransfer.files[0]));
  renderTabela();

/* ---------- Tabela de preços do gás (outra concessionária) ---------- */
  const temGlobalGas = (g) => finito(g.termo) || finito(g.preco);

  function linhaDaTabelaGas() {
    const t = state.tabelaGas;
    const esc = state.gas.perfil.escalao;
    if (finito(esc) && esc > 0) {
      const l = (t.linhas || []).find((x) => finito(x.escalao) && x.escalao === esc);
      if (l) return l;
    }
    return null;
  }

  // Preenche a proposta de gás com a linha do escalão do cliente (ou com os valores comuns).
  function aplicarTabelaGas() {
    estadoTabelaGas();
    const t = state.tabelaGas, m = state.gas, uso = $("#tabela-uso-gas");
    if (!t.linhas.length && !temGlobalGas(t.global)) { uso.textContent = ""; return; }
    const esc = m.perfil.escalao;
    let l = linhaDaTabelaGas();
    if (!l && t.linhas.length === 1) l = t.linhas[0];
    if (!l && t.linhas.length > 1 && !temGlobalGas(t.global)) {
      uso.textContent = finito(esc) && esc > 0
        ? "A tabela não tem o escalão " + esc + ": preencha a proposta à mão ou adicione essa linha."
        : "Escolha o escalão de consumo do cliente para aplicar a tabela.";
      return;
    }
    const g = t.global || {};
    const val = (a, b) => (finito(a) && a > 0 ? a : finito(b) && b > 0 ? b : null);
    const p = m.proposta, partes = [];
    if (t.nome) p.nome = t.nome;
    p.potDia = val(l && l.termo, g.termo);
    p.precoSimples = val(l && l.preco, g.preco);
    if (p.potDia !== null) partes.push("termo fixo " + fmtN(p.potDia, 4) + " €/dia");
    if (p.precoSimples !== null) partes.push("energia " + fmtN(p.precoSimples, 4) + " €/kWh");
    if (finito(g.descontoPct)) { p.descontoPct = g.descontoPct; partes.push("desconto " + fmtN(g.descontoPct, 2) + "%"); }
    uso.textContent = (l ? "Linha usada" + (l.escalao ? " (escalão " + l.escalao + ")" : "") : "Valores comuns da tabela") + ": " + partes.join(" · ") + ".";
  }

  function estadoTabelaGas() {
    const n = state.tabelaGas.linhas.length;
    $("#tabela-estado-gas").textContent = n ? n + (n === 1 ? " escalão" : " escalões") : temGlobalGas(state.tabelaGas.global) ? "só valores comuns" : "sem tabela";
    const ativa = (state.tabelaGas.linhas || []).indexOf(linhaDaTabelaGas());
    $$("#tabela-corpo-gas tr").forEach((tr, i) => tr.classList.toggle("ativa", i === ativa));
  }

  function celulaTabGas(i, k, v, rot) {
    return '<td><input type="number" inputmode="decimal" step="any" min="0" data-gi="' + i + '" data-gk="' + k + '" aria-label="' + rot + '" value="' + (finito(v) ? v : "") + '"></td>';
  }

  function renderTabelaGas() {
    $("#tabela-corpo-gas").innerHTML = state.tabelaGas.linhas.map((l, i) =>
      "<tr>" + celulaTabGas(i, "escalao", l.escalao, "Escalão") + celulaTabGas(i, "termo", l.termo, "Termo fixo em euros por dia") + celulaTabGas(i, "preco", l.preco, "Energia em euros por kWh") +
      '<td><button type="button" class="rm" data-grm="' + i + '" aria-label="Remover linha">×</button></td></tr>').join("");
    const g = state.tabelaGas.global;
    $("#tgg-nome").value = state.tabelaGas.nome || "";
    $("#tgg-termo").value = finito(g.termo) ? g.termo : "";
    $("#tgg-preco").value = finito(g.preco) ? g.preco : "";
    $("#tgg-desc").value = finito(g.descontoPct) ? g.descontoPct : "";
    estadoTabelaGas();
  }

  function reaplicarTabelaGas() {
    aplicarTabelaGas();
    preencher();
    atualizar();
    estadoTabelaGas();
  }

  $("#tabela-corpo-gas").addEventListener("input", (e) => {
    const el = e.target;
    if (el.dataset.gi === undefined) return;
    const n = parseFloat(String(el.value).replace(",", "."));
    state.tabelaGas.linhas[Number(el.dataset.gi)][el.dataset.gk] = Number.isFinite(n) ? n : null;
    reaplicarTabelaGas();
  });
  $("#tabela-corpo-gas").addEventListener("click", (e) => {
    const b = e.target.closest("[data-grm]");
    if (!b) return;
    state.tabelaGas.linhas.splice(Number(b.dataset.grm), 1);
    renderTabelaGas();
    reaplicarTabelaGas();
  });
  $("#btn-add-linha-gas").addEventListener("click", () => {
    state.tabelaGas.linhas.push({ escalao: null, termo: null, preco: null });
    renderTabelaGas();
    const campos = $$("#tabela-corpo-gas tr:last-child input");
    if (campos[0]) campos[0].focus();
  });
  $("#btn-limpar-tabela-gas").addEventListener("click", () => {
    state.tabelaGas = TABELA_GAS_DEFAULT();
    renderTabelaGas();
    $("#leitura-tabela-gas").innerHTML = "";
    $("#tabela-uso-gas").textContent = "";
    atualizar();
    toast("Tabela de gás limpa");
  });
  $$("[data-tgg]").forEach((el) => el.addEventListener("input", () => {
    const n = parseFloat(String(el.value).replace(",", "."));
    state.tabelaGas.global[el.dataset.tgg] = Number.isFinite(n) ? n : null;
    reaplicarTabelaGas();
  }));
  $("#tgg-nome").addEventListener("input", (e) => {
    state.tabelaGas.nome = e.target.value;
    reaplicarTabelaGas();
  });

  async function lerTabelaGas(ficheiro) {
    if (!ficheiro) return;
    const alvo = $("#leitura-tabela-gas");
    const msg = (cls, html) => { alvo.innerHTML = '<div class="' + cls + '">' + html + "</div>"; };
    if (!leituraDisponivel) {
      msg("leitura-erro", "A leitura de ficheiros precisa do servidor Go (<b>go run main.go</b>). Pode preencher a tabela à mão, aqui em baixo.");
      return;
    }
    if (ficheiro.size > 20 * 1024 * 1024) { msg("leitura-erro", "Ficheiro demasiado grande (máximo 20 MB)."); return; }
    msg("leitura-aguarde", "A ler <b>" + esc(ficheiro.name || "tabela") + "</b>…");
    try {
      const fd = new FormData();
      fd.append("ficheiro", ficheiro, ficheiro.name || "tabela");
      const r = await fetch("api/ler-texto", { method: "POST", body: fd });
      const j = await r.json().catch(() => ({}));
      if (!r.ok || j.erro) throw new Error(j.erro || "Erro " + r.status);
      const t = analisarTabelaGas(j.texto);
      const detalhe = '<details><summary>Ver texto lido do ficheiro</summary><pre class="texto-lido">' + esc(String(j.texto).slice(0, 8000)) + "</pre></details>";
      if (!t.linhas.length && !temGlobalGas(t.global)) {
        if (await propostaDeFatura(ficheiro, "gas", msg)) return;
        msg("leitura-erro", "Não encontrei preços de gás por escalão nem uma fatura de gás neste ficheiro. Preencha a tabela à mão ou confira o texto lido." + detalhe);
        return;
      }
      state.tabelaGas = { nome: t.nome, linhas: t.linhas, global: t.global };
      renderTabelaGas();
      reaplicarTabelaGas();
      let h = "<b>Tabela de gás lida" + (t.nome ? " · " + esc(t.nome) : "") + "</b> ";
      h += '<span class="chip">' + ({ ocr: "leitura local (OCR)" }[j.metodo] || "leitura local") + "</span>";
      h += "<p>" + t.linhas.length + " escalão(ões)" + (t.linhas.length ? ": " + esc(t.linhas.map((l) => l.escalao).join(", ")) : "") + ". Confirme os valores na tabela abaixo.</p>";
      if (t.avisos.length) h += "<p>" + esc(t.avisos.join(" ")) + "</p>";
      alvo.innerHTML = '<div class="leitura-ok">' + h + detalhe + "</div>";
    } catch (e) {
      msg("leitura-erro", esc(e.message) + "<br>Pode preencher a tabela à mão.");
    }
  }

  const dzG = $("#dropzone-tabela-gas"), inputTabelaGas = $("#ficheiro-tabela-gas");
  inputTabelaGas.addEventListener("change", () => { lerTabelaGas(inputTabelaGas.files[0]); inputTabelaGas.value = ""; });
  ["dragenter", "dragover"].forEach((ev) => dzG.addEventListener(ev, (e) => { e.preventDefault(); dzG.classList.add("sobre"); }));
  ["dragleave", "drop"].forEach((ev) => dzG.addEventListener(ev, (e) => { e.preventDefault(); dzG.classList.remove("sobre"); }));
  dzG.addEventListener("drop", (e) => lerTabelaGas(e.dataTransfer.files[0]));
  renderTabelaGas();

  $$('[data-s="proposta"][data-num]').forEach((el) => { el.placeholder = "0,00"; });
  $$('[data-s="proposta"][data-k="nome"]').forEach((el) => { el.placeholder = "Nome da concessionária"; });

  /* Tema */
  function aplicarTema() {
    const escuro = state.tema ? state.tema === "escuro" : window.matchMedia("(prefers-color-scheme: dark)").matches;
    document.documentElement.dataset.tema = escuro ? "escuro" : "claro";
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = escuro ? "#0b1522" : "#0f2a43";
  }
  $("#btn-tema").addEventListener("click", () => {
    state.tema = document.documentElement.dataset.tema === "escuro" ? "claro" : "escuro";
    aplicarTema();
    gravar();
  });

  /* Estado da rede + PWA */
  function rede() {
    const pill = $("#pill-rede");
    pill.textContent = navigator.onLine ? "online" : "offline";
    pill.classList.toggle("off", !navigator.onLine);
  }
  window.addEventListener("online", rede);
  window.addEventListener("offline", rede);

  if ("serviceWorker" in navigator && (location.protocol === "https:" || location.hostname === "localhost")) {
    window.addEventListener("load", () => {
      navigator.serviceWorker.register("sw.js").then(() => navigator.serviceWorker.ready).then(() => {
        try {
          if (!localStorage.getItem("calc-energia-pt:offline-ok")) {
            localStorage.setItem("calc-energia-pt:offline-ok", "1");
            toast("Pronto para funcionar offline");
          }
        } catch (e) {}
      }).catch(() => {});
    });
  }

  /* Arranque */
  aplicarTema();
  rede();
  preencher();
  atualizar();      // resultado imediato com o motor JS
  iniciarMotor();   // depois tenta o motor Go (WASM)
})();
`

const engineJS = `/* Motor de cálculo em JavaScript — espelho exato de engine/engine.go.
   Usado como fallback quando o servidor Go não está acessível. */
(function (root) {
  "use strict";
  const r2 = (x) => Math.round(x * 100) / 100;
  const pos = (x) => (Number.isFinite(x) && x > 0 ? x : 0);
  const clamp = (x, lo, hi) => Math.min(Math.max(x, lo), hi);

  function linha(chave, grupo, base, pct) {
    base = r2(base);
    const iva = r2((base * pct) / 100);
    return { chave, grupo, base, ivaPct: pct, iva, total: r2(base + iva) };
  }

  function calcular(q) {
    const dias = Math.max(pos(q.dias), 1);
    const consumo = pos(q.consumoKwh);
    const desc = clamp(Number(q.descontoPct) || 0, 0, 100);
    const f = q.fiscal;
    const linhas = [];

    if (q.tipo === "gas") {
      linhas.push(
        linha("potencia", "antes", pos(q.potDia) * dias, f.gasIva),
        linha("energia", "antes", consumo * pos(q.precoSimples) * (1 - desc / 100), f.gasIva),
        linha("iec", "impostos", consumo * pos(f.gasIspKwh), f.gasIva),
        linha("dgeg", "impostos", pos(f.gasDgegMes), f.gasIva)
      );
    } else {
      const potPct = q.potenciaKva <= f.potReduzidaMaxKva ? f.ivaReduzido : f.ivaNormal;
      const potBruta = pos(q.potDia) * dias * (q.descontoPotencia ? 1 - desc / 100 : 1);
      const pot = linha("potencia", "antes", potBruta, potPct);

      let bruta;
      if (q.tarifa === "bi") {
        const v = clamp(Number(q.vazioPct) || 0, 0, 100) / 100;
        bruta = consumo * v * pos(q.precoVazio) + consumo * (1 - v) * pos(q.precoForaVazio);
      } else {
        bruta = consumo * pos(q.precoSimples);
      }
      const base = r2(bruta * (1 - desc / 100));
      let frac = 0;
      if (q.potenciaKva <= f.energiaReduzidaMaxKva && consumo > 0) {
        frac = Math.min(consumo, (pos(f.limiteKwhReduzido) * dias) / 30) / consumo;
      }
      const enPct = frac * f.ivaReduzido + (1 - frac) * f.ivaNormal;
      const en = linha("energia", "antes", base, enPct);

      linhas.push(
        pot, en,
        linha("iec", "impostos", consumo * pos(f.iecKwh), f.ivaNormal),
        linha("dgeg", "impostos", pos(f.dgegMes), f.ivaNormal)
      );
      if (f.cavAplica) linhas.push(linha("cav", "impostos", pos(f.cavMes), f.ivaReduzido));
    }

    if (q.outrosMes > 0) {
      linhas.push(linha("outros", "extra", pos(q.outrosMes), q.tipo === "gas" ? f.gasIva : f.ivaNormal));
    }

    const res = { linhas, subtotal: 0, impostos: 0, extras: 0, iva: 0, total: 0 };
    let soma = 0;
    for (const l of linhas) {
      if (l.grupo === "antes") res.subtotal += l.base;
      else if (l.grupo === "impostos") res.impostos += l.base;
      else res.extras += l.base;
      res.iva += l.iva;
      soma += l.base;
    }
    res.subtotal = r2(res.subtotal);
    res.impostos = r2(res.impostos);
    res.extras = r2(res.extras);
    res.iva = r2(res.iva);
    res.total = r2(soma + res.iva);
    return res;
  }

  const api = { calcular };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.JSEngine = api;
})(typeof self !== "undefined" ? self : this);
`

const swJS = `/* Service worker: funcionamento offline (cache do "app shell"). */
const VERSAO = "calc-energia-pt-v14";
const FICHEIROS = [
  "./", "index.html", "style.css", "app.js", "engine.js",
  "manifest.webmanifest",
  "icon.svg", "icon-192.png", "icon-512.png",
];

self.addEventListener("install", (e) => {
  e.waitUntil(
    caches.open(VERSAO).then((c) =>
      // addAll falharia se um ficheiro faltasse (ex.: ficheiro em falta)
      Promise.all(FICHEIROS.map((f) => c.add(f).catch(() => null)))
    ).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((ks) => Promise.all(ks.filter((k) => k !== VERSAO).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

// Stale-while-revalidate: responde da cache e atualiza em segundo plano.
self.addEventListener("fetch", (e) => {
  const req = e.request;
  const u = new URL(req.url);
  if (req.method !== "GET" || u.origin !== location.origin || u.pathname.startsWith("/api/")) return;
  e.respondWith(
    caches.open(VERSAO).then(async (cache) => {
      const emCache = await cache.match(req, { ignoreSearch: true });
      const rede = fetch(req).then((r) => {
        if (r && r.ok) cache.put(req, r.clone());
        return r;
      }).catch(() => null);
      return emCache || (await rede) || (req.mode === "navigate" ? cache.match("index.html") : Response.error());
    })
  );
});
`

const manifestJSON = `{
  "name": "Simulador de Poupança Energética",
  "short_name": "Energia PT",
  "description": "Compare eletricidade e gás natural em tempo real.",
  "lang": "pt-PT",
  "start_url": "./index.html",
  "scope": "./",
  "display": "standalone",
  "background_color": "#0f2a43",
  "theme_color": "#0f2a43",
  "icons": [
    { "src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any maskable" },
    { "src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable" },
    { "src": "icon.svg", "sizes": "any", "type": "image/svg+xml" }
  ]
}
`

const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" fill="#0f2a43"/><path d="M290 70 150 290h92l-32 152 152-226h-96z" fill="#35c28f"/></svg>
`

const icon192 = "iVBORw0KGgoAAAANSUhEUgAAAMAAAADACAIAAADdvvtQAAADx0lEQVR4nO3bwVEzRxSF0cYJkIUXpMHWgTgkB+JoHI0X+gtjgaTR3B6k1++cCGbx1e1uFby8vr0P2Ou3R38AtQmIiICICIiIgIgIiIiAiAiIiICICIiIgIgIiIiAiAiIiICICIiIgIgIiIiAiAiIiICICIiIgIgIiIiAiAhogt//+uPRn/AwAkp1rmcIiJCAIs3nZwgo8VHPP3/+/dgveSABERHQTg6vEwEREdAe5ueDgO6mns8ElOr8BBsCupf5OSOgO6jnKwEREdBW5udbAtpEPZcIKNL8CTYEtIX5uUJARAR0g/m5TkDXqOcmARER0EU358cTbAjoEofXRgIiIqBvmJ/tBEREQOfMz10E9D/b6/EEOxEQEQH9x+G1g4B+Uc8+AiIioDHMT0BAe+rxBPsgICLdA3J4hboHRKh1QOYn1zcg9UzRN6DdPME+axqQ+ZmlY0DqmahjQEzULiDzM1evgNQzXa+Acp5gZxoFZH6O0CggjtAlIPNzkBYBqec4LQLiOOsHNHF+PMG+Wjwgh9fRFg+Io728vr0/+huOUnp+qhyXyy6Qen7GsgHxM9YMyPz8mDUDqqtWPWPJgErPTzmrBVS6nnLzM9YLqK6K9YzFAio9P0WtE1DpeorOz1gpoLrq1jOWCaj0/JS2SEB1lZ6fsUZAdeenej1jgYDU81jlA+Kxagdkfh6ucEDqeQaFA+IZVA3I/DyJkgGp53mUDIjnUS8g8/NU6gVU1JL1jHIB1Z2fVVUKqG49q87PqBVQUQvXMwoFVHd+1rby/8bvMD3TtednFFqgipavZwjoM6fkDgI6Sof5GQI6SJN6hoAICWi+PvMzBPRh1g26VT1DQIQENFO3+RkCmqhhPUNAJ35C3E1Ac/ScnyGgKdrWMwSU61zPEBAhAUU36ObzMwSUUM8QECEB7WR+TgS0h3o+dA/Ib9Ch7gHtYH4+E9B91HNGQEQEdAfz81XrgO66QavnW60DIiegTczPJQK6TT1X9A3IT4hT9A1oI/NznYCuUc9NAiIioIvMzxZNA7p5g1bPRk0DYhYBfcP8bCegc+q5i4CIdAzoyg3a/NyrY0CXqGcHAf2inn0ERERAY5ifQLuAvt6g1ZNoFxBzdQ/I/IRaB6SeXK+A/BnrdL0C+sz8TNE0IPXM0jQgZukYkPmZqFFApxu0euZqFBBH6BWQ+Znu5fXt/dHfQGG9FojpBEREQEQERERARAREREBEBEREQEQERERARAREREBEBEREQEQERERARAREREBEBEREQEQERERARAREREBE/gUcX8tjug3D+AAAAABJRU5ErkJggg=="

const icon512 = "iVBORw0KGgoAAAANSUhEUgAAAgAAAAIACAIAAAB7GkOtAAALp0lEQVR4nO3dwZUbRxJFUVBHe3oxC7rB7Rgik2SIrBlrtOCcJtmNBqoKWZUZ8e+1oFfxvpAl6cvXb99vAOT5Y/YfAMAcAgAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAIJQAAoQQAIJQAAIQSAIBQAgAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAIJQAAoQQAIJQAAIQSAIBQAgAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAIJQBwuv/8/d/ZfwLcIQBwLtefZQkAQCgBgBOZ/6xMAOAsb9f/f3/9M/cvgbsEACCUAMAp/PjD+gQAIJQAwHjmPyUIAAzm+lOFAMC5fALEsgQARjL/KUQAYBjXn1oEACCUAMAY5j/lCAAM4PpTkQDAiXwCxMoEAF5l/lOUAACEEgB4iflPXQIAx7n+lCYAAKEEAA56Ov99AsTiBACO8OMPDQgAQCgBgN3Mf3oQANjH9acNAQAIJQCww/b57xMg1icAAKEEALby6z/NCABs4vrTjwAAhBIAeM78pyUBgCcOXH+fAFGCAACEEgB4xI8/NCYA8CnXn94EACCUAMB95j/tCQAM5hMgqhAAuMP8J4EAwHuuPyEEACCUAMBvzH9yCAD85PoTRQBgJJ8AUYgAwP+Z/6QRALjdXH8iCQBAKAEA859QAgAQSgBIN3D++wSIWgSAaH78IZkAAIQSAHKZ/4QTAEK5/iAAAKEEgERnzH+fAFGOABDHjz/wgwAAhBIAspj/8EYAAEIJAEHMf/iVAJDi1OvvEyAqEgCAUAJABD/+wEcCQH+uP9wlAAChBIDmzH/4jADQ2TXX3ydAFCUAAKEEgLb8+AOPCQBAKAGgJ/MfnhIAGnL9YQsBAAglAHRz8fz3DSh1CQCt+PEHthMAgFACQB/mP+wiADTh+sNeAgAQSgDoYNb89wkQpQkAQCgBoDy//sMxAkBtrj8cJgAAoQSAwsx/eIUAUNX06+8TIKoTAIBQAkBJ0+c/NCAA1OP6wxACABBKACjG/IdRBACO8AkQDQgAlZj/MJAAUIbrD2MJAEAoAaAG8x+GEwAKcP3hDAIAu/kEiB4EgNWZ/3ASAWBprj+cRwAAQgkA6zL/4VQCABBKAFjUsvPfJ0C0IQCsaNnrD50IAEAoAWA55j9cQwBYi+sPlxEAgFACwELWn/8+AaITAWAV619/aEYAAEIJAEsw/+F6AgAQSgCYz/yHKQSAyQpdf58A0YwAAIQSAGYqNP+hHwFgGtcf5hIAgFACwBzmP0wnAExQ8fr7BIh+BAAglABwtYrzH1oSAIBQAsClzH9YhwBwHdcfliIA8JxPgGhJALiI+Q+rEQCu4PrDggQAIJQAcDrzH9YkAJzL9YdlCQA84RMguhIATmT+w8oEACCUAHAW8x8WJwCcwvWH9QkAQCgBYLxO898nQDQmAAzW6fpDbwIAEOrL12/fZ/8N9GH+p/ETWWn+CYBhXP80rn91AgAQSgAYw/xPY/43IADAbq5/DwLAAOY/VCQAvMr1T2P+tyEAwA6ufycCwEvMf6hLADjO9U9j/jcjAMAmrn8/AsBB5j9UJwAc4fqnMf9bEgDgCde/KwFgN/MfehAA4BHzvzEBYB/zP4rr35sAsIPrD50IAHCf+d+eALCV+R/F9U8gAGzi+kM/AgC8Z/6HEACeM/+juP45BIAnXH/oSgCAn8z/KALAI+Z/FNc/jQAAhBIAPmX+RzH/AwkA97n+UVz/TAIA6Vz/WALAHeY/JBAA3nP9o5j/yQQAcrn+4QSA35j/kEMA+Mn1j2L+IwCQyPXnJgC8Mf8hjQBAHPOfHwSA2838T+L680YAcP0hlABAEPOfXwlAOvM/h+vPOwIQzfWHZAIAEcx/PhKAXOZ/DtefuwQglOsPCAA0Z/7zGQFIZP7ncP15QAAAQglAHPM/h/nPYwKQxfXP4frzlAAAhBKAIOZ/DvOfLQQgheufw/VnIwEACCUAEcz/HOY/2wlAf65/DtefXQQAIJQANGf+5zD/2UsAoAPXnwMEoDPzH3hAANpy/XOY/xwjAFCb689hAtCT+Q88JQANuf45zH9eIQBQlevPiwSgG/Mf2EgAWnH9c5j/vE4AoB7XnyEEoA/zH9hFAKAY859RBKAJ8z+E689AAtCB6w8cIABQhvnPWAJQnvkfwvVnuD9n/wG8yl34SBRhC/8EAAXIPGcQALrpN/9df04iAAChBACWZv5zHgGAdbn+nEoAAEIJAK10egE2/zmbAMCKXH8uIAAAoQQAlmP+cw0BoI8eDwCuP5cRAFiI68+VBAAglADAKsx/LiYAsATXn+sJAE30eAGGKwkAzGf+M4UAwGSuP7MIAEAoAYCZzH8mEgA6KPoC7PozlwAAhBIAmMP8ZzoBgAlcf1YgAAChBIDyyr0Am/8sQgDgUq4/6xAAgFACANcx/1mKAFBboQcA15/VCABAKAGAK5j/LEgA4HSuP2sSAIBQAkBhJV6AzX+WJQBwIteflQkAQCgBgLOY/yxOAOAUrj/rEwCqKvECDCsTABjP/KcEAYDBXH+qEACAUAIAI5n/FCIAlLTmC7DrTy0CABBKAGAM859yBAAGcP2pSACoZ80HAChHAOBV5j9FCQC8xPWnLgEACCUAcJz5T2kCQDHrvAC7/lQnAAChBACOMP9pQABgN9efHgQAIJQAUMkKL8DmP20IAOzg+tOJAACEEgDYyvynGQGATVx/+hEAyljhBRg6EQB4zvynJQGAJ1x/uhIAeMT1pzEBoAYPADCcAMCnzH96EwC4z/WnPQEACCUAcIf5TwIBoICLX4Bdf0IIAEAoAYDfmP/kEAD4yfUnigAAhBIAVnfZC7D5TxoBgNvN9SeSAACEEgAw/wklAKRz/YklACzNfwUaziMARDP/SSYA5HL9CScAAKEEgHWd+gBg/oMAkMj1h5sAAMQSAOKY//CDAJDF9Yc3AsCi/CtgcDYBIIj5D78SAFK4/vCOAACEEgAimP/wkQCworEvwK4/3CUAAKEEgObMf/iMANCZ6w8PCABAKAFgOaNegM1/eEwA6Mn1h6cEACCUANCQ+Q9bCABref0BwPWHjQQAIJQA0Ir5D9sJAH24/rCLAACEEgAW8soLsPkPewkAHbj+cIAAAIQSAMoz/+EYAaA21x8OEwBWMfZ/Awk8JQAUZv7DKwSAqlx/eJEAAIQSAEoy/+F1AsASdr0Au/4whAAAhBIAijH/YRQBoBLXHwYSAObb+ADg+sNYAgAQSgCowfyH4QSAAlx/OIMAAIQSACZ7+gJs/sNJBICluf5wHgEACCUArMv8h1MJAIty/eFsAsBM/jeQMJEAsCLzHy4gACzH9YdrCABAKAFgLeY/XEYAmObjC7DrD1cSAIBQAsAqzH+4mACwBNcfricAzOFfAYPpBID5zH+YQgCYzPWHWQQAIJQAMJP5DxMJABP8eAF2/WEuAQAIJQDMYf7DdF++fvs++28AYAL/BAAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAIJQAAoQQAIJQAAIQSAIBQAgAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAIJQAAoQQAIJQAAIQSAIBQAgAQSgAAQgkAQCgBAAglAAChBAAglAAAhBIAgFACABBKAABCCQBAKAEACCUAAKEEACCUAACEEgCAUAIAEEoAAEIJAEAoAQAI9S+Nmhdqlk4ufAAAAABJRU5ErkJggg=="
