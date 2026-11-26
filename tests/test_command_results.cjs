// Pure source-function regression tests. No browser, network or robot access.
const fs=require('node:fs'),path=require('node:path'),vm=require('node:vm'),assert=require('node:assert/strict');
const source=fs.readFileSync(path.resolve(__dirname,'../agent/ui/app.js'),'utf8');
const match=source.match(/export function assertCommand\([^]*?\n\}/);
assert(match,'command validator missing');
const context=vm.createContext({});vm.runInContext(match[0].replace('export ',''),context);
for(const [method,params] of [['action',{siid:6,aiid:2}],['local.thresholds',{}]]){
 for(const result of [{code:0},{code:0,out:[]},{code:0,out:null},{code:0,out:[{piid:6,value:-1}]},{code:0,out:[{piid:6,value:'0'}]},{code:0,out:[{piid:6,value:0},{piid:6,value:0}]}]){
  assert.throws(()=>context.assertCommand(result,method,params));
 }
 context.assertCommand({code:0,out:[{piid:7,value:1},{piid:6,value:0}]},method,params);
}
context.assertCommand({code:0,out:[]},'action',{siid:4,aiid:1});
context.assertCommand([{code:0}],'set_properties',[{siid:4,piid:4,value:1}]);
assert.throws(()=>context.assertCommand({code:-1},'action',{siid:4,aiid:1}));
console.log('PASS command-result regressions; no network or robot operations');
