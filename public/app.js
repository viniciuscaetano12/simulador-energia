/* Interface do simulador — cálculo em tempo real.
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
      case "potencia": return `${fmtN(f.potDia)} €/dia × ${fmtN(p.dias, 0)} dias`;
      case "energia": {
        const d = f.descontoPct > 0 ? ` − ${fmtN(f.descontoPct, 2)}%` : "";
        if (modo === "eletricidade" && p.tarifa === "bi")
          return `${fmtN(p.consumoKwh, 1)} kWh (${fmtN(p.vazioPct, 0)}% vazio)${d}`;
        return `${fmtN(p.consumoKwh, 1)} × ${fmtN(f.precoSimples)} €/kWh${d}`;
      }
      case "iec": return modo === "gas" ? `${fmtN(p.consumoKwh, 1)} kWh × ${fmtN(fi.gasIspKwh)} €` : `${fmtN(p.consumoKwh, 1)} kWh × ${fmtN(fi.iecKwh)} €`;
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
    document.title = `Proposta energética${state.meta.cliente ? " - " + state.meta.cliente : ""}`;
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
