package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// Categoria representa uma aba do resultado final
type Categoria struct {
	Nome string
	URL  string
}

// Produto representa cada item extraído da vitrine
type Produto struct {
	Nome          string
	Preco         string  // texto formatado, ex: "R$ 34,90" ou "Indisponível"
	PrecoNumerico float64 // valor numérico pra cálculo de porcentagem (0 se indisponível)
	TemPreco      bool
	ImagemUrl     string
	EmEstoque     bool
	Link          string
}

// Resultado agrupa os produtos encontrados de uma categoria
type Resultado struct {
	Categoria Categoria
	Produtos  []Produto
}

func main() {
	categorias := []Categoria{
		{Nome: "Rosh", URL: "https://www.novapanda.com.br/rosh"},
		{Nome: "Narguiles Completos", URL: "https://www.novapanda.com.br/narguiles-completos"},
		{Nome: "Vasos", URL: "https://www.novapanda.com.br/vasos-de-narguile"},
	}

	var resultados []Resultado

	for _, cat := range categorias {
		fmt.Printf("\n=== %s ===\n", cat.Nome)

		var todos []Produto
		vistos := make(map[string]bool)

		for pagina := 1; pagina <= 20; pagina++ {
			url := cat.URL
			if pagina > 1 {
				url = fmt.Sprintf("%s?pg=%d", cat.URL, pagina)
			}
			fmt.Println("Buscando produtos em:", url)

			produtos, err := obterProdutos(url)
			if err != nil {
				log.Fatalf("Erro ao buscar produtos: %v", err)
			}

			novos := 0
			for _, p := range produtos {
				if !vistos[p.Link] {
					vistos[p.Link] = true
					todos = append(todos, p)
					novos++
				}
			}

			fmt.Printf("  página %d: %d produtos novos\n", pagina, novos)

			if novos == 0 {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		resultados = append(resultados, Resultado{Categoria: cat, Produtos: todos})
	}

	totalGeral := 0
	for _, r := range resultados {
		totalGeral += len(r.Produtos)
	}
	if totalGeral == 0 {
		fmt.Println("Nenhum produto foi encontrado em nenhuma categoria.")
		return
	}

	if err := gerarHTML(resultados, "produtos.html"); err != nil {
		log.Fatalf("Erro ao gerar HTML: %v", err)
	}

	fmt.Printf("\n%d produtos salvos em produtos.html\n", totalGeral)
}

func obterProdutos(targetURL string) ([]Produto, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	utf8Reader, err := decodificarUTF8(resp)
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(utf8Reader)
	if err != nil {
		return nil, err
	}

	var produtos []Produto
	rePreco := regexp.MustCompile(`R\$\s*([\d.,]+)(?:\s+R\$\s*([\d.,]+))?`)

	doc.Find(".showcase-catalog li").Each(func(i int, card *goquery.Selection) {
		linkEl := card.Find("a[href]").First()
		href, existeHref := linkEl.Attr("href")
		if !existeHref || href == "" {
			return
		}

		// Nome vem do alt da imagem (não contamina com badge de desconto)
		var imgEl *goquery.Selection
		card.Find("img").EachWithBreak(func(j int, im *goquery.Selection) bool {
			if alt, ok := im.Attr("alt"); ok && strings.TrimSpace(alt) != "" {
				imgEl = im
				return false
			}
			return true
		})
		if imgEl == nil {
			return
		}
		nome := strings.TrimSpace(imgEl.AttrOr("alt", ""))
		if nome == "" {
			return
		}

		semEstoque := card.Find(".seal-not-stock").Length() > 0

		// Tenta achar o preço no texto que vem depois do nome.
		// Produtos sem estoque muitas vezes não mostram preço — nesse caso
		// o produto ainda é incluído, só marcado como "Indisponível".
		textoCompleto := strings.Join(strings.Fields(card.Text()), " ")
		var textoApos string
		if idx := strings.Index(textoCompleto, nome); idx >= 0 {
			textoApos = textoCompleto[idx+len(nome):]
		} else {
			textoApos = textoCompleto
		}

		precoTexto := "Indisponível"
		var precoNumerico float64
		temPreco := false

		m := rePreco.FindStringSubmatch(textoApos)
		if m != nil {
			valorStr := m[1]
			if m[2] != "" {
				valorStr = m[2] // preço promocional, se houver
			}
			if valor, err := parseBRLParaFloat(valorStr); err == nil {
				precoNumerico = valor
				precoTexto = "R$ " + valorStr
				temPreco = true
			}
		}

		// Imagem: prioriza data-src (lazyload real) sobre src (placeholder)
		imagemUrl := imgEl.AttrOr("data-src", "")
		if imagemUrl == "" {
			imagemUrl = imgEl.AttrOr("data-original", "")
		}
		if imagemUrl == "" {
			imagemUrl = imgEl.AttrOr("src", "")
		}
		if strings.HasPrefix(imagemUrl, "//") {
			imagemUrl = "https:" + imagemUrl
		}

		produtos = append(produtos, Produto{
			Nome:          nome,
			Preco:         precoTexto,
			PrecoNumerico: precoNumerico,
			TemPreco:      temPreco,
			ImagemUrl:     imagemUrl,
			EmEstoque:     !semEstoque,
			Link:          href,
		})
	})

	return produtos, nil
}

// parseBRLParaFloat converte "1.234,56" -> 1234.56
func parseBRLParaFloat(s string) (float64, error) {
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	return strconv.ParseFloat(s, 64)
}

// decodificarUTF8 detecta o charset declarado e converte para UTF-8 (o site usa ISO-8859-1)
func decodificarUTF8(resp *http.Response) (interface{ Read([]byte) (int, error) }, error) {
	e, _, _ := charset.DetermineEncoding(nil, resp.Header.Get("Content-Type"))
	if e == nil {
		e = charmap.ISO8859_1
	}
	return transform.NewReader(resp.Body, e.NewDecoder()), nil
}

func gerarHTML(resultados []Resultado, caminho string) error {
	var abas strings.Builder
	var paineis strings.Builder

	for i, r := range resultados {
		ativoTab := ""
		ativoPainel := "display:none;"
		if i == 0 {
			ativoTab = " ativo"
			ativoPainel = ""
		}

		disponiveis := 0
		for _, p := range r.Produtos {
			if p.EmEstoque {
				disponiveis++
			}
		}
		foraEstoque := len(r.Produtos) - disponiveis

		abas.WriteString(fmt.Sprintf(
			`<button class="tab%s" onclick="abrirAba(%d, this)">%s <span class="contagem">%d disp. · %d esgot.</span></button>`,
			ativoTab, i, html.EscapeString(r.Categoria.Nome), disponiveis, foraEstoque,
		))

		var cards strings.Builder
		for _, p := range r.Produtos {
			status := "esgotado"
			statusTexto := "Esgotado"
			cardClasse := "card indisponivel"
			if p.EmEstoque {
				status = "ok"
				statusTexto = "Em estoque"
				cardClasse = "card"
			}

			dataPreco := ""
			precoNovoHTML := ""
			if p.TemPreco {
				dataPreco = fmt.Sprintf(` data-preco="%.2f"`, p.PrecoNumerico)
				precoNovoHTML = `<p class="preco-novo"></p>`
			}

			cards.WriteString(fmt.Sprintf(`
      <div class="%s"%s>
        <img src="%s" alt="%s" onclick="abrirImagem('%s')">
        <h3>%s</h3>
        <p class="preco">%s</p>
        %s
        <span class="status %s">%s</span>
      </div>`,
				cardClasse,
				dataPreco,
				html.EscapeString(p.ImagemUrl),
				html.EscapeString(p.Nome),
				html.EscapeString(p.ImagemUrl),
				html.EscapeString(p.Nome),
				html.EscapeString(p.Preco),
				precoNovoHTML,
				status,
				statusTexto,
			))
		}

		paineis.WriteString(fmt.Sprintf(`
    <div class="painel" id="painel-%d" style="%s">
      <div class="grid">%s
      </div>
    </div>`, i, ativoPainel, cards.String()))
	}

	saida := fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-br">
<head>
<meta charset="UTF-8">
<title>Produtos Nova Panda</title>
<style>
  body { font-family: sans-serif; background: #111; color: #eee; padding: 20px; }

  .percentual-control {
    background: #1e1e1e;
    border: 1px solid #333;
    border-radius: 8px;
    padding: 14px 18px;
    margin-bottom: 20px;
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .percentual-control label { font-weight: bold; }
  .percentual-control input {
    background: #111;
    border: 1px solid #444;
    color: #eee;
    border-radius: 6px;
    padding: 8px 10px;
    width: 100px;
    font-size: 1em;
  }
  .percentual-control span.dica { color: #888; font-size: 0.9em; }

  .tabs { display: flex; gap: 8px; margin-bottom: 20px; flex-wrap: wrap; }
  .tab { background: #1e1e1e; color: #aaa; border: 1px solid #333; padding: 10px 16px; border-radius: 6px; cursor: pointer; font-size: 0.95em; text-align: left; }
  .tab .contagem { display: block; font-size: 0.75em; color: #777; margin-top: 2px; }
  .tab.ativo { background: #2563eb; color: #fff; border-color: #2563eb; }
  .tab.ativo .contagem { color: #cfe0ff; }

  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 16px; }
  .card { background: #1e1e1e; border-radius: 8px; padding: 12px; text-align: center; }
  .card.indisponivel { opacity: 0.55; }
  .card img { width: 100%%; height: 150px; object-fit: contain; background: #fff; border-radius: 4px; cursor: pointer; transition: transform 0.15s; }
  .card img:hover { transform: scale(1.03); }
  .preco { color: #4ade80; font-weight: bold; font-size: 1.1em; margin: 6px 0 2px; }
  .card.indisponivel .preco { color: #888; }
  .preco-novo { color: #facc15; font-size: 0.95em; margin: 0 0 6px; min-height: 1.2em; }
  .status { display: inline-block; padding: 4px 8px; border-radius: 4px; font-size: 0.85em; }
  .status.ok { background: #14532d; color: #4ade80; }
  .status.esgotado { background: #450a0a; color: #f87171; }

  .overlay {
    display: none;
    position: fixed;
    top: 0; left: 0; width: 100%%; height: 100%%;
    background: rgba(0,0,0,0.85);
    z-index: 999;
    justify-content: center;
    align-items: center;
    cursor: zoom-out;
  }
  .overlay.aberto { display: flex; }
  .overlay img { max-width: 90%%; max-height: 90%%; border-radius: 8px; }
  .overlay .fechar { position: absolute; top: 20px; right: 30px; color: #fff; font-size: 2em; cursor: pointer; }
</style>
</head>
<body>
  <h1>Produtos Nova Panda</h1>

  <div class="percentual-control">
    <label for="percentualInput">Adicionar %%:</label>
    <input type="number" id="percentualInput" value="0" step="0.1" oninput="atualizarPrecos()">
    <span class="dica">informe uma porcentagem pra ver o preço final ao lado do valor original</span>
  </div>

  <div class="tabs">%s</div>

  %s

  <div class="overlay" id="overlay" onclick="fecharImagem()">
    <span class="fechar">&times;</span>
    <img id="overlayImg" src="" alt="">
  </div>

  <script>
    function abrirAba(indice, botao) {
      document.querySelectorAll('.painel').forEach(function(p) { p.style.display = 'none'; });
      document.querySelectorAll('.tab').forEach(function(t) { t.classList.remove('ativo'); });
      document.getElementById('painel-' + indice).style.display = '';
      botao.classList.add('ativo');
    }
    function abrirImagem(src) {
      document.getElementById('overlayImg').src = src;
      document.getElementById('overlay').classList.add('aberto');
    }
    function fecharImagem() {
      document.getElementById('overlay').classList.remove('aberto');
    }
    document.addEventListener('keydown', function(e) {
      if (e.key === 'Escape') fecharImagem();
    });

    function atualizarPrecos() {
      var pct = parseFloat(document.getElementById('percentualInput').value) || 0;
      document.querySelectorAll('.card[data-preco]').forEach(function(card) {
        var original = parseFloat(card.getAttribute('data-preco'));
        var alvo = card.querySelector('.preco-novo');
        if (!alvo) return;
        if (pct === 0) {
          alvo.textContent = '';
          return;
        }
        var novo = original * (1 + pct / 100);
        alvo.textContent = '+' + pct + '%% -> R$ ' + novo.toFixed(2).replace('.', ',');
      });
    }

    atualizarPrecos();
  </script>
</body>
</html>`, abas.String(), paineis.String())

	return os.WriteFile(caminho, []byte(saida), 0644)
}
