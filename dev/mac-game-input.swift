import AppKit
import CoreGraphics
import Foundation
import Darwin

func fail(_ text: String) -> Never { FileHandle.standardError.write(Data((text+"\n").utf8)); exit(1) }
let args = CommandLine.arguments
func windows(_ pid: Int32) -> [[String: Any]] {
    guard let list = CGWindowListCopyWindowInfo([.optionAll, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] else { fail("window inventory unavailable") }
    return list.filter { ($0[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == pid }
}
func emit(_ value: Any) {
    guard JSONSerialization.isValidJSONObject(value), let raw=try? JSONSerialization.data(withJSONObject:value,options:[.sortedKeys]) else { fail("invalid output") }
    FileHandle.standardOutput.write(raw); print("")
}
guard args.count >= 3, let pid=Int32(args[2]), pid > 1 else { fail("usage: pilot windows PID | key PID WINDOW KEY MILLISECONDS") }
let rows=windows(pid)
if args[1] == "windows" { emit(rows); exit(0) }
guard args.count >= 4, let window=Int(args[3]) else { fail("exact game window required") }
let matches=rows.filter { ($0[kCGWindowNumber as String] as? NSNumber)?.intValue == window }
let gameWindows=rows.filter { (($0[kCGWindowName as String] as? String) ?? "").lowercased().contains("world of warcraft") }
guard gameWindows.count == 1 else { fail("one eligible game window per process required") }
guard matches.count == 1, let name=matches[0][kCGWindowName as String] as? String, name.lowercased().contains("warcraft") else { fail("exact game window required") }

if args[1] == "text" {
    guard args.count == 5, args[4].count <= 256 else { fail("bounded text required") }
    let codes:[String:CGKeyCode] = ["a":0,"b":11,"c":8,"d":2,"e":14,"f":3,"g":5,"h":4,"i":34,"j":38,"k":40,"l":37,"m":46,"n":45,"o":31,"p":35,"q":12,"r":15,"s":1,"t":17,"u":32,"v":9,"w":13,"x":7,"y":16,"z":6," ":49,"/":44,"0":29,"1":18,"2":19,"3":20,"4":21,"5":23,"6":22,"7":26,"8":28,"9":25,".":47,"-":27]
    guard args[4].allSatisfy({codes[String($0).lowercased()] != nil}), AXIsProcessTrusted(), let app=NSRunningApplication(processIdentifier:pid) else { fail("supported text and input permission required") }
    app.activate(options:[])
    for character in args[4] {
        let c=String(character), code=codes[c.lowercased()]!
        guard let down=CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:true), let up=CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:false) else { fail("key event unavailable") }
        if c != c.lowercased() { down.flags = .maskShift; up.flags = .maskShift }
        down.postToPid(pid); up.postToPid(pid); Thread.sleep(forTimeInterval:0.012)
    }
    emit(["pid":pid,"window":window,"characters":args[4].count]); exit(0)
}
if args[1] == "click" {
    guard args.count == 7, let x=Double(args[5]), let y=Double(args[6]), x.isFinite, y.isFinite,
          let b=matches[0][kCGWindowBounds as String] as? [String:Double],
          let bx=b["X"],let by=b["Y"],let width=b["Width"],let height=b["Height"],
          x >= 0, y >= 0, x < width, y < height, ["left","right"].contains(args[4]),
          AXIsProcessTrusted(), let app=NSRunningApplication(processIdentifier:pid) else { fail("bounded game click required") }
    app.activate(options:[])
    let right=args[4]=="right", point=CGPoint(x:bx+x,y:by+y), button:CGMouseButton=right ? .right:.left
    guard let move=CGEvent(mouseEventSource:nil,mouseType:.mouseMoved,mouseCursorPosition:point,mouseButton:button),
          let down=CGEvent(mouseEventSource:nil,mouseType:right ? .rightMouseDown:.leftMouseDown,mouseCursorPosition:point,mouseButton:button),
          let up=CGEvent(mouseEventSource:nil,mouseType:right ? .rightMouseUp:.leftMouseUp,mouseCursorPosition:point,mouseButton:button) else { fail("mouse event unavailable") }
    move.postToPid(pid); down.postToPid(pid); up.postToPid(pid)
    emit(["pid":pid,"window":window,"button":args[4],"x":x,"y":y]); exit(0)
}
guard args[1] == "key", args.count == 6, let milliseconds=Int(args[5]), milliseconds >= 20, milliseconds <= 1500 else { fail("bounded key command required") }

let keys:[String:CGKeyCode] = ["w":13,"a":0,"s":1,"d":2,"q":12,"e":14,"space":49,"escape":53,"return":36,"tab":48,"1":18,"2":19,"3":20,"4":21,"5":23,"6":22,"7":26,"8":28,"9":25,"0":29,"b":11,"l":37,"m":46,"f":3,"up":126,"down":125,"left":123,"right":124]
guard let key=keys[args[4]], kill(pid,0)==0, let app=NSRunningApplication(processIdentifier:pid) else { fail("known live game PID and allowlisted key required") }
guard AXIsProcessTrusted() else { fail("macOS Accessibility permission unavailable for helper") }
app.activate(options:[]); Thread.sleep(forTimeInterval:0.08)
guard let down=CGEvent(keyboardEventSource:nil,virtualKey:key,keyDown:true), let up=CGEvent(keyboardEventSource:nil,virtualKey:key,keyDown:false) else { fail("keyboard event unavailable") }
down.postToPid(pid); Thread.sleep(forTimeInterval:Double(milliseconds)/1000); up.postToPid(pid)
emit(["pid":pid,"window":window,"key":args[4],"heldMs":milliseconds,"released":true])
