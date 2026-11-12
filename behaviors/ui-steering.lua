-- Pure waypoint feedback; Python owns observed-body guards and paired input.
local function n(v) return tonumber(v) or 0 end
local function d(a,b)
    return math.sqrt((n(a.x)-n(b.x))^2+(n(a.y)-n(b.y))^2+(n(a.z)-n(b.z))^2)
end
local function segment(p,a,b)
    local x,y,z=n(b.x)-n(a.x),n(b.y)-n(a.y),n(b.z)-n(a.z)
    local size=x*x+y*y+z*z
    local t=0
    if size>0 then t=math.max(0,math.min(1,((n(p.x)-n(a.x))*x+(n(p.y)-n(a.y))*y+(n(p.z)-n(a.z))*z)/size)) end
    return d(p,{x=n(a.x)+t*x,y=n(a.y)+t*y,z=n(a.z)+t*z})
end
return {decide=function(s)
    local p,path=s.pose,s.path
    assert(type(p)=='table' and type(path)=='table' and #path>=2 and #path<=256,'bounded physical path required')
    local i=math.max(1,math.min(#path,math.floor(n(s.index))))
    local drift=1000000000
    for j=math.max(1,i-1),math.min(#path-1,i+1) do drift=math.min(drift,segment(p,path[j],path[j+1])) end
    if drift>1.5 then return {phase='path_drift',index=i} end
    while i<#path and d(p,path[i])<=0.65 do i=i+1 end
    local remaining=d(p,path[i])
    for j=i,#path-1 do remaining=remaining+d(path[j],path[j+1]) end
    if d(p,path[#path])<=n(s.arrivalDistance) and remaining<=n(s.arrivalDistance)+0.1 then
        return {phase='arrived',arrived=true,index=i}
    end
    local target=path[i]
    local dx,dy=n(target.x)-n(p.x),n(target.y)-n(p.y)
    assert(dx*dx+dy*dy>0.0001,'vertical-only waypoint unsupported')
    local pi=3.141592653589793
    local error=(n(s.bearings[i])-n(p.orientation)+pi)%(2*pi)-pi
    if math.abs(error)>0.12 then
        local ms=math.max(20,math.min(250,math.floor(math.abs(error)/n(s.turnRate)*1000)))
        return {phase='turning',index=i,key=error>0 and 'left' or 'right',durationMs=ms}
    end
    local ms=math.max(20,math.min(250,math.floor(math.sqrt(dx*dx+dy*dy)/n(s.runSpeed)*1000)))
    return {phase='walking',index=i,key='w',durationMs=ms}
end}
