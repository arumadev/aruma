// measure.sh's Expo app: the one line of @aruma/web for Metro. ARUMA_E2E_SVG=before|after also
// applies react-native-svg-transformer's documented Expo config before or after it: before is the
// supported order (the rewrite wraps the SVG transformer); after replaces the rewrite's transformer,
// which the bundle must refuse.
const { getDefaultConfig } = require('expo/metro-config');
const { withArumaStorage } = require('@aruma/web/metro');

function withSvg(config) {
  const { transformer, resolver } = config;
  return {
    ...config,
    transformer: { ...transformer, babelTransformerPath: require.resolve('react-native-svg-transformer/expo') },
    resolver: { ...resolver, assetExts: resolver.assetExts.filter((e) => e !== 'svg'), sourceExts: [...resolver.sourceExts, 'svg'] },
  };
}

const svg = process.env.ARUMA_E2E_SVG;
let config = getDefaultConfig(__dirname);
if (svg === 'before') config = withSvg(config);
config = withArumaStorage(config, { url: process.env.ARUMA_URL });
if (svg === 'after') config = withSvg(config);
module.exports = config;
