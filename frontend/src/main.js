import "./style.css";
import { Rooms, Version } from "../bindings/github.com/ciranda-dev/kolo-group-wails3-example/groupservice.js";

const list = document.getElementById("rooms");
const empty = document.getElementById("empty");

Version().then((v) => {
  document.getElementById("version").textContent = v;
});

Rooms().then((rooms) => {
  empty.hidden = rooms.length > 0;
  for (const room of rooms) {
    const li = document.createElement("li");
    li.textContent = room.name;
    list.append(li);
  }
});
