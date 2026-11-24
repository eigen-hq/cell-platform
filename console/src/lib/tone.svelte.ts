// The console's tone: night or paper. The system setting decides unless the
// user picked one here; the pick is kept in this browser.
const KEY = "cell.tone";
type Tone = "night" | "paper";

const system = matchMedia("(prefers-color-scheme: light)");
let picked = $state<Tone | null>(localStorage.getItem(KEY) as Tone | null);
let systemTone = $state<Tone>(system.matches ? "paper" : "night");
system.addEventListener("change", () => (systemTone = system.matches ? "paper" : "night"));

export const tone = {
  get current(): Tone {
    return picked ?? systemTone;
  },
  toggle() {
    picked = this.current === "night" ? "paper" : "night";
    localStorage.setItem(KEY, picked);
    document.documentElement.dataset.tone = picked;
  },
};

if (picked) document.documentElement.dataset.tone = picked;
