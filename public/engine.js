/* Motor de cálculo em JavaScript — espelho exato de engine/engine.go.
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
