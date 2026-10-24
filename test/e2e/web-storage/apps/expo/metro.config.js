// measure.sh's Expo app: the one line of @aruma/web for Metro.
const { getDefaultConfig } = require('expo/metro-config');
const { withArumaStorage } = require('@aruma/web/metro');

module.exports = withArumaStorage(getDefaultConfig(__dirname), { url: process.env.ARUMA_URL });
