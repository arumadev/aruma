import * as app from 'firebase-11/app';
import * as auth from 'firebase-11/auth';
import * as database from 'firebase-11/database';
import { start } from './app.js';

start(app, auth, database);
