import { mount } from "svelte";
import "./lib/ui/global.css";
import App from "./App.svelte";

mount(App, { target: document.getElementById("app")! });
