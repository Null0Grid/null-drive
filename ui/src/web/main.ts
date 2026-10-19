import { mount } from 'svelte';
import '../lib/styles/fonts';
import '../lib/styles/tokens.css';
import '../lib/styles/base.css';
import './app.css';
import App from './App.svelte';

mount(App, { target: document.getElementById('app')! });
