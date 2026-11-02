// Bobbin as the presenter's cursor: it follows the mouse, trails a
// thread, squishes and throws thread confetti on clicks, bobs when idle,
// now and then spins, and wobbles while you type. For recordings only.
(() => {
  const SVG = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 260 300" width="62" height="72" aria-hidden="true">   <defs><filter id="bbn-sticker" x="-10%" y="-10%" width="120%" height="120%">   <feMorphology in="SourceAlpha" operator="dilate" radius="5" result="grow"/>   <feFlood flood-color="#F7F2E7"/><feComposite in2="grow" operator="in" result="edge"/>   <feMerge><feMergeNode in="edge"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs> <style> .o{stroke:#1B2236;stroke-width:5;stroke-linejoin:round;stroke-linecap:round} .wood{fill:#B07A4F}.wood2{fill:#8C5B36} .t1{fill:#E4B640}.t2{fill:#C8506A}.t3{fill:#4C79A8}.t4{fill:#EEE8DA} .thr{fill:none;stroke-width:6;stroke-linecap:round} </style> <g filter="url(#bbn-sticker)"> <!-- the loose thread trailing behind, looping like a weft pick --> <path class="thr" stroke="#1B2236" stroke-width="11" d="M62 186 C 20 196, 14 240, 46 250 C 78 260, 84 226, 60 222 C 40 219, 34 262, 70 276 C 100 287, 150 284, 196 280"/> <path class="thr" stroke="#C8506A" d="M62 186 C 20 196, 14 240, 46 250 C 78 260, 84 226, 60 222 C 40 219, 34 262, 70 276 C 100 287, 150 284, 196 280"/> <!-- feet --> <ellipse class="o wood2" cx="104" cy="268" rx="20" ry="11"/> <ellipse class="o wood2" cx="156" cy="268" rx="20" ry="11"/> <!-- bottom flange --> <path class="o wood" d="M54 238 Q130 270 206 238 L206 252 Q130 284 54 252 Z"/> <!-- thread body, wound in stripes --> <clipPath id="bbn-body"><path d="M66 82 Q130 100 194 82 L194 240 Q130 258 66 240 Z"/></clipPath> <g clip-path="url(#bbn-body)">   <rect x="60" y="70" width="140" height="200" class="t4"/>   <path class="t1" d="M60 96 Q130 116 200 96 L200 124 Q130 144 60 124 Z"/>   <path class="t2" d="M60 124 Q130 144 200 124 L200 140 Q130 160 60 140 Z"/>   <path class="t3" d="M60 196 Q130 216 200 196 L200 214 Q130 234 60 214 Z"/>   <path class="t1" d="M60 214 Q130 234 200 214 L200 226 Q130 246 60 226 Z"/>   <path class="t2" d="M60 226 Q130 246 200 226 L200 270 Q130 290 60 270 Z"/>   <!-- fine winding lines -->   <g stroke="#1B2236" stroke-opacity=".12" stroke-width="2" fill="none">     <path d="M60 106 Q130 126 200 106"/><path d="M60 116 Q130 136 200 116"/><path d="M60 132 Q130 152 200 132"/>     <path d="M60 204 Q130 224 200 204"/><path d="M60 220 Q130 240 200 220"/><path d="M60 236 Q130 256 200 236"/><path d="M60 248 Q130 268 200 248"/>   </g> </g> <path class="o" fill="none" d="M66 82 Q130 100 194 82 L194 240 Q130 258 66 240 Z"/> <!-- top flange --> <ellipse class="o wood" cx="130" cy="74" rx="78" ry="22"/> <ellipse class="wood2" cx="130" cy="70" rx="20" ry="7"/> <!-- face, on the linen band --> <ellipse cx="104" cy="170" rx="12" ry="15" fill="#1B2236"/> <ellipse cx="156" cy="170" rx="12" ry="15" fill="#1B2236"/> <circle cx="108" cy="164" r="4.5" fill="#fff"/><circle cx="160" cy="164" r="4.5" fill="#fff"/> <ellipse cx="86" cy="188" rx="11" ry="6.5" fill="#E98AA0" opacity=".85"/> <ellipse cx="174" cy="188" rx="11" ry="6.5" fill="#E98AA0" opacity=".85"/> <path class="o" fill="none" d="M118 186 Q130 197 142 186"/> <!-- arms: thread loops, the right one waving --> <path class="thr" stroke="#1B2236" stroke-width="11" d="M66 176 C 46 182, 40 196, 46 206"/><path class="thr" stroke="#4C79A8" stroke-width="5" d="M66 176 C 46 182, 40 196, 46 206"/> <circle cx="46" cy="208" r="8" class="o t1"/> <path class="thr" stroke="#1B2236" stroke-width="11" d="M194 160 C 214 150, 222 130, 216 112"/><path class="thr" stroke="#4C79A8" stroke-width="5" d="M194 160 C 214 150, 222 130, 216 112"/> <circle cx="216" cy="108" r="8" class="o t1"/> <path fill="none" stroke="#E4B640" stroke-width="4" stroke-linecap="round" d="M232 98 q8 -6 6 -16 M236 116 q10 0 14 -8"/> </g> </svg> ';
  const install = () => {
    if (document.getElementById("bbn")) return;
    const st = document.createElement("style");
    st.textContent = `
      #bbn{position:fixed;left:0;top:0;width:62px;height:72px;z-index:2147483647;pointer-events:none;transform:translate(-200px,-200px);will-change:transform}
      #bbn .b{transform-origin:31px 66px;transition:transform .18s cubic-bezier(.3,1.6,.5,1)}
      #bbn.down .b{transform:scale(1.18,.74)}
      #bbn.idle .b{animation:bbn-bob 1.1s ease-in-out infinite}
      #bbn.spin .b{animation:bbn-spin .9s cubic-bezier(.5,0,.3,1)}
      #bbn.typing .b{animation:bbn-wob .28s ease-in-out infinite}
      @keyframes bbn-bob{0%,100%{transform:translateY(0)}50%{transform:translateY(-6px) rotate(-4deg)}}
      @keyframes bbn-spin{from{transform:rotate(0)}to{transform:rotate(360deg)}}
      @keyframes bbn-wob{0%,100%{transform:rotate(-7deg)}50%{transform:rotate(7deg)}}
      #bbn-thread{position:fixed;inset:0;z-index:2147483646;pointer-events:none}
      .bbn-bit{position:fixed;z-index:2147483646;pointer-events:none;width:12px;height:4px;border-radius:2px}
      .bbn-dot{position:fixed;z-index:2147483647;pointer-events:none;width:8px;height:8px;margin:-4px 0 0 -4px;border-radius:50%;background:#C8506A;box-shadow:0 0 0 2px #F7F2E7}
    `;
    document.documentElement.appendChild(st);
    const el = document.createElement("div"); el.id = "bbn"; el.innerHTML = `<div class="b">${SVG}</div>`;
    const dot = document.createElement("div"); dot.className = "bbn-dot"; dot.style.left = "-50px";
    const ns = "http://www.w3.org/2000/svg";
    const thread = document.createElementNS(ns, "svg"); thread.id = "bbn-thread";
    const line = document.createElementNS(ns, "path");
    line.setAttribute("fill", "none"); line.setAttribute("stroke", "#C8506A"); line.setAttribute("stroke-width", "3");
    line.setAttribute("stroke-linecap", "round"); line.setAttribute("stroke-linejoin", "round"); line.setAttribute("opacity", ".55");
    thread.appendChild(line);
    document.documentElement.append(thread, el, dot);
    let x = -200, y = -200, idleT, spinT, typeT;
    const pts = [];
    const place = () => { el.style.transform = `translate(${x + 5}px, ${y + 7}px)`; dot.style.left = x + "px"; dot.style.top = y + "px"; };
    const drawThread = () => {
      const now = performance.now();
      while (pts.length && now - pts[0].t > 700) pts.shift();
      line.setAttribute("d", pts.length > 1 ? "M" + pts.map((p) => `${p.x + 30},${p.y + 66}`).join(" L") : "");
      requestAnimationFrame(drawThread);
    };
    requestAnimationFrame(drawThread);
    const settle = () => {
      el.classList.remove("idle"); clearTimeout(idleT); clearTimeout(spinT);
      idleT = setTimeout(() => {
        el.classList.add("idle");
        spinT = setTimeout(() => { el.classList.remove("idle"); el.classList.add("spin"); setTimeout(() => { el.classList.remove("spin"); el.classList.add("idle"); }, 900); }, 2600);
      }, 1200);
    };
    addEventListener("mousemove", (e) => { x = e.clientX; y = e.clientY; pts.push({ x, y, t: performance.now() }); place(); settle(); }, true);
    addEventListener("mousedown", () => {
      el.classList.add("down");
      const colors = ["#E4B640", "#C8506A", "#4C79A8", "#EEE8DA"];
      for (let i = 0; i < 9; i++) {
        const b = document.createElement("div"); b.className = "bbn-bit"; b.style.background = colors[i % 4];
        b.style.left = x + "px"; b.style.top = y + "px"; document.documentElement.appendChild(b);
        const a = (Math.PI * 2 * i) / 9 + Math.random() * 0.4, r = 28 + Math.random() * 22;
        b.animate([{ transform: `rotate(${a}rad) translateX(4px)`, opacity: 1 }, { transform: `rotate(${a + 0.6}rad) translateX(${r}px)`, opacity: 0 }],
          { duration: 520, easing: "cubic-bezier(.2,.7,.3,1)" }).onfinish = () => b.remove();
      }
    }, true);
    addEventListener("mouseup", () => el.classList.remove("down"), true);
    addEventListener("keydown", () => { el.classList.add("typing"); clearTimeout(typeT); typeT = setTimeout(() => el.classList.remove("typing"), 350); }, true);
    settle();
  };
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", install); else install();
})();
